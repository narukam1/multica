package execenv

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const workspaceLayoutRelPath = ".index/workspace-layout.yaml"

// WorkspaceLayoutParams is the daemon-side request to materialise a composite
// working tree from a reference checkout (nested git repos + junctions).
type WorkspaceLayoutParams struct {
	LocalPath       string
	EnvRoot         string
	WorkspacesRoot  string
	WorkspaceID     string
	WorkspaceSlug   string
	AgentName       string
	TaskID          string
	IssueID         string
	IssueIdentifier string
	// IssueTitle / IssueDescription are the claimed issue body. on_demand
	// task_relevant_only and child branch.child_repos={tb_id} read these;
	// comments and project text are not a substitute.
	IssueTitle       string
	IssueDescription string
	// IssueParentID / IssueParentIdentifier are the issue row's parent
	// (not quick-create "file under"). One-hop inherit when no ancestor
	// list is present.
	IssueParentID         string
	IssueParentIdentifier string
	// LayoutAncestors is the claimed issue's parent chain (facts only).
	// The daemon applies yaml ident prefixes; the server does not pick dest.
	LayoutAncestors []LayoutAncestor
	// LayoutOwnerID / LayoutOwnerIdentifier are claim-compat fields from
	// older servers. Dest resolution ignores them.
	LayoutOwnerID         string
	LayoutOwnerIdentifier string
	// RelevantRepos are on_demand paths this task should materialise when
	// the layout file says include: task_relevant_only. Prefixes come from
	// yaml on_demand.git_worktree.path_prefixes, not from Multica.
	RelevantRepos []string
	// ImplementPrefixes / ProductPrefixes come from yaml ident.*. Empty
	// means "no tree-owning ident": children inherit dest/branch.
	ImplementPrefixes []string
	ProductPrefixes   []string
}

// LayoutKey is the dest/branch/lock identity for a workspace_layout tree.
type LayoutKey struct {
	IssueID         string
	IssueIdentifier string
}

// LayoutAncestor is one hop of the claimed issue's parent chain. The
// server lists identities; dest ownership is decided on the daemon.
type LayoutAncestor struct {
	IssueID          string `json:"issue_id,omitempty"`
	IssueIdentifier  string `json:"issue_identifier,omitempty"`
	ParentID         string `json:"parent_id,omitempty"`
	ParentIdentifier string `json:"parent_identifier,omitempty"`
}

// LookupLayoutAncestors returns a WalkLayoutOwner lookup over a claim chain.
func LookupLayoutAncestors(ancestors []LayoutAncestor) func(id string) (ident, parentID, parentIdent string, ok bool) {
	byID := map[string]LayoutAncestor{}
	for _, a := range ancestors {
		id := strings.TrimSpace(a.IssueID)
		if id == "" {
			continue
		}
		byID[id] = a
	}
	return func(id string) (ident, parentID, parentIdent string, ok bool) {
		a, ok := byID[strings.TrimSpace(id)]
		if !ok {
			return "", "", "", false
		}
		return a.IssueIdentifier, a.ParentID, a.ParentIdentifier, true
	}
}

// WorkspaceLayout is the prepared composite tree. Finalize leaves the tree
// mounted for the next run. Whether leftovers are auto-committed is
// workspace-layout.yaml agent.finalize_commit (default chore).
type WorkspaceLayout struct {
	WorkDir        string
	Members        []workspaceLayoutMember
	Branch         string
	finalizeCommit string
	prepared       bool
}

type workspaceLayoutMember struct {
	RelPath       string
	SrcGit        string
	DestPath      string
	Branch        string
	Kind          string // git_worktree | junction
	Created       bool   // this prepare created the mount (rollback may drop it)
	CreatedBranch bool   // this prepare created the git branch
}

type workspaceLayoutFile struct {
	Always []struct {
		Path      string `yaml:"path"`
		Isolation string `yaml:"isolation"`
		Source    string `yaml:"source"`
		Role      string `yaml:"role"`
	} `yaml:"always"`
	Ident struct {
		ImplementPrefixes []string `yaml:"implement_prefixes"`
		ProductPrefixes   []string `yaml:"product_prefixes"`
	} `yaml:"ident"`
	OnDemand struct {
		GitWorktree struct {
			Catalog              string   `yaml:"catalog"`
			Include              string   `yaml:"include"`
			PathPrefixes         []string `yaml:"path_prefixes"`
			NestedMustWithParent []struct {
				Parent string `yaml:"parent"`
				Child  string `yaml:"child"`
			} `yaml:"nested_must_with_parent"`
		} `yaml:"git_worktree"`
		Junction []struct {
			When   string `yaml:"when"`
			Path   string `yaml:"path"`
			Source string `yaml:"source"`
		} `yaml:"junction"`
	} `yaml:"on_demand"`
	Branch struct {
		ChildRepos string `yaml:"child_repos"`
		Root       string `yaml:"root"`
	} `yaml:"branch"`
	Agent workspaceLayoutAgent `yaml:"agent"`
}

// workspaceLayoutAgent is project-owned Multica run policy. Names of
// skills are never listed here — dest already has them.
type workspaceLayoutAgent struct {
	AdvertiseWorkdirSkills bool   `yaml:"advertise_workdir_skills"`
	AdvertiseOn            string `yaml:"advertise_on"` // dest_worktree | always
	Brief                  string `yaml:"brief"`
	FinalizeCommit         string `yaml:"finalize_commit"`
}

// PrepareWorkspaceLayout builds envRoot/workspace from the reference tree.
func PrepareWorkspaceLayout(params WorkspaceLayoutParams, logger *slog.Logger) (*WorkspaceLayout, error) {
	root := filepath.Clean(params.LocalPath)
	if err := validateReferenceRoot(root); err != nil {
		return nil, err
	}
	cfg, err := loadWorkspaceLayoutFile(root)
	if err != nil {
		return nil, err
	}
	params = applyIdentPolicy(params, cfg)
	destRoot := IssueLayoutWorkDir(params)
	if destRoot == "" {
		return nil, fmt.Errorf("workspace_layout: dest root is empty")
	}
	if err := os.MkdirAll(filepath.Dir(destRoot), 0o755); err != nil {
		return nil, fmt.Errorf("workspace_layout: create dest parent: %w", err)
	}
	branch := layoutBranchName(params)
	childBranch := childLayoutBranchName(params, cfg)
	wl := &WorkspaceLayout{
		WorkDir:        destRoot,
		Branch:         branch,
		finalizeCommit: normalizeFinalizeCommit(cfg.Agent.FinalizeCommit),
	}

	if err := materialiseAlways(root, destRoot, branch, cfg, wl, logger); err != nil {
		wl.rollbackPrepare(logger)
		return nil, err
	}
	if err := materialiseOnDemand(root, destRoot, childBranch, cfg, params.RelevantRepos, wl, logger); err != nil {
		wl.rollbackPrepare(logger)
		return nil, err
	}
	wl.prepared = true
	return wl, nil
}

func validateReferenceRoot(root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("workspace_layout: reference path %s: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace_layout: reference path is not a directory: %s", root)
	}
	if _, ok := detectGitRepo(root); !ok {
		return fmt.Errorf("workspace_layout: reference path is not a git repository: %s", root)
	}
	return nil
}

func loadWorkspaceLayoutFile(root string) (workspaceLayoutFile, error) {
	path := filepath.Join(root, filepath.FromSlash(workspaceLayoutRelPath))
	raw, err := os.ReadFile(path)
	if err != nil {
		return workspaceLayoutFile{}, fmt.Errorf(
			"workspace_layout: missing %s (composite projects must declare git_worktree + junction members)",
			workspaceLayoutRelPath)
	}
	var cfg workspaceLayoutFile
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return workspaceLayoutFile{}, fmt.Errorf("workspace_layout: parse %s: %w", workspaceLayoutRelPath, err)
	}
	return cfg, nil
}

// LayoutPolicy is the project-owned ident and path prefix set from
// workspace-layout.yaml. Empty slices mean Multica must not guess.
type LayoutPolicy struct {
	ImplementPrefixes []string
	ProductPrefixes   []string
	PathPrefixes      []string
}

// LoadLayoutPolicy reads ident.* and on_demand.git_worktree.path_prefixes.
func LoadLayoutPolicy(root string) (LayoutPolicy, error) {
	cfg, err := loadWorkspaceLayoutFile(root)
	if err != nil {
		return LayoutPolicy{}, err
	}
	return layoutPolicyFromFile(cfg), nil
}

func layoutPolicyFromFile(cfg workspaceLayoutFile) LayoutPolicy {
	return LayoutPolicy{
		ImplementPrefixes: sanitizeIdentPrefixes(cfg.Ident.ImplementPrefixes),
		ProductPrefixes:   sanitizeIdentPrefixes(cfg.Ident.ProductPrefixes),
		PathPrefixes:      sanitizeIdentPrefixes(cfg.OnDemand.GitWorktree.PathPrefixes),
	}
}

func applyIdentPolicy(params WorkspaceLayoutParams, cfg workspaceLayoutFile) WorkspaceLayoutParams {
	policy := layoutPolicyFromFile(cfg)
	if len(params.ImplementPrefixes) == 0 {
		params.ImplementPrefixes = policy.ImplementPrefixes
	}
	if len(params.ProductPrefixes) == 0 {
		params.ProductPrefixes = policy.ProductPrefixes
	}
	return params
}

var identPrefixToken = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,31}$`)

func sanitizeIdentPrefixes(raw []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if !identPrefixToken.MatchString(p) {
			continue
		}
		key := strings.ToLower(p)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}
	return out
}

func identPrefixes(params WorkspaceLayoutParams) []string {
	return sanitizeIdentPrefixes(append(append([]string{}, params.ImplementPrefixes...), params.ProductPrefixes...))
}

func prefixAlt(prefixes []string) string {
	parts := sanitizeIdentPrefixes(prefixes)
	if len(parts) == 0 {
		return ""
	}
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = regexp.QuoteMeta(p)
	}
	return strings.Join(quoted, "|")
}

func exactIdentRegexp(prefixes []string) *regexp.Regexp {
	alt := prefixAlt(prefixes)
	if alt == "" {
		return nil
	}
	return regexp.MustCompile(`(?i)^(?:` + alt + `)(?:-[0-9]+)+$`)
}

func identInTextRegexp(prefixes []string) *regexp.Regexp {
	alt := prefixAlt(prefixes)
	if alt == "" {
		return nil
	}
	return regexp.MustCompile(`(?i)\b((?:` + alt + `)(?:-[0-9]+)+)\b`)
}

func repoPathRegexp(pathPrefixes []string) *regexp.Regexp {
	alt := prefixAlt(pathPrefixes)
	if alt == "" {
		return nil
	}
	return regexp.MustCompile(`(?i)\b((?:` + alt + `)/[A-Za-z0-9._-]+)`)
}

// ResolveLayoutKey picks the dest/branch owner for a workspace_layout run.
// A distinct tree-owning identifier (yaml ident prefixes) on this issue
// keeps its own tree; otherwise a step child inherits the parent.
// Empty prefixes inherit. LayoutOwner* is ignored — dest is yaml + parent hops.
func ResolveLayoutKey(params WorkspaceLayoutParams) LayoutKey {
	if len(params.LayoutAncestors) > 0 {
		return WalkLayoutOwnerWithIdentPrefixes(
			params.IssueID,
			params.IssueIdentifier,
			identPrefixes(params),
			LookupLayoutAncestors(params.LayoutAncestors),
		)
	}
	return resolveLayoutKeyOneHop(params)
}

func resolveLayoutKeyOneHop(params WorkspaceLayoutParams) LayoutKey {
	childID := strings.TrimSpace(params.IssueID)
	childIdent := strings.TrimSpace(params.IssueIdentifier)
	parentID := strings.TrimSpace(params.IssueParentID)
	if parentID == "" {
		return LayoutKey{IssueID: childID, IssueIdentifier: childIdent}
	}
	prefixes := identPrefixes(params)
	childTB := tbIdentKey(childIdent, prefixes)
	parentTB := tbIdentKey(params.IssueParentIdentifier, prefixes)
	if childTB != "" && childTB != parentTB {
		return LayoutKey{IssueID: childID, IssueIdentifier: childIdent}
	}
	parentIdent := strings.TrimSpace(params.IssueParentIdentifier)
	if parentIdent == "" {
		parentIdent = childIdent
	}
	return LayoutKey{IssueID: parentID, IssueIdentifier: parentIdent}
}

// WalkLayoutOwner follows parent hops until ResolveLayoutKey stops inheriting.
// lookup returns that issue's identifier and its parent id/ident.
// Without ident prefixes every child inherits (Vega step issues).
func WalkLayoutOwner(issueID, issueIdent string, lookup func(id string) (ident, parentID, parentIdent string, ok bool)) LayoutKey {
	return WalkLayoutOwnerWithIdentPrefixes(issueID, issueIdent, nil, lookup)
}

// WalkLayoutOwnerWithIdentPrefixes is WalkLayoutOwner using project ident prefixes.
func WalkLayoutOwnerWithIdentPrefixes(issueID, issueIdent string, prefixes []string, lookup func(id string) (ident, parentID, parentIdent string, ok bool)) LayoutKey {
	currentID := strings.TrimSpace(issueID)
	currentIdent := strings.TrimSpace(issueIdent)
	seen := map[string]struct{}{}
	for hop := 0; hop < 16; hop++ {
		if currentID == "" {
			return LayoutKey{IssueIdentifier: currentIdent}
		}
		if _, loop := seen[currentID]; loop {
			return LayoutKey{IssueID: currentID, IssueIdentifier: currentIdent}
		}
		seen[currentID] = struct{}{}
		ident, parentID, parentIdent, ok := lookup(currentID)
		if strings.TrimSpace(ident) != "" {
			currentIdent = strings.TrimSpace(ident)
		}
		if !ok || strings.TrimSpace(parentID) == "" {
			return LayoutKey{IssueID: currentID, IssueIdentifier: currentIdent}
		}
		key := resolveLayoutKeyOneHop(WorkspaceLayoutParams{
			IssueID:               currentID,
			IssueIdentifier:       currentIdent,
			IssueParentID:         parentID,
			IssueParentIdentifier: parentIdent,
			ImplementPrefixes:     prefixes,
		})
		if key.IssueID == currentID {
			return key
		}
		currentID = key.IssueID
		currentIdent = key.IssueIdentifier
	}
	return LayoutKey{IssueID: currentID, IssueIdentifier: currentIdent}
}

func applyLayoutKey(params WorkspaceLayoutParams) WorkspaceLayoutParams {
	key := ResolveLayoutKey(params)
	params.IssueID = key.IssueID
	params.IssueIdentifier = key.IssueIdentifier
	return params
}

func tbIdentKey(raw string, prefixes []string) string {
	re := exactIdentRegexp(prefixes)
	if re == nil {
		return ""
	}
	raw = strings.TrimSpace(raw)
	if !re.MatchString(raw) {
		return ""
	}
	return normalizeTBIdent(raw)
}

// IssueLayoutWorkDir is the composite-tree dest. When the issue identity is
// known it is stable across tasks of that issue (workspace + issue, not task).
// Official env roots stay per-task via PredictRootDir. Step children share
// the inherited layout owner's dest.
func IssueLayoutWorkDir(params WorkspaceLayoutParams) string {
	params = applyLayoutKey(params)
	if params.WorkspacesRoot != "" && params.WorkspaceID != "" && strings.TrimSpace(params.IssueID) != "" {
		return filepath.Join(
			params.WorkspacesRoot,
			readablePathSegment(params.WorkspaceSlug, "workspace", params.WorkspaceID),
			readablePathSegment(params.IssueIdentifier, "issue", params.IssueID),
			"workspace",
		)
	}
	if params.EnvRoot == "" {
		return ""
	}
	return filepath.Join(params.EnvRoot, "workspace")
}

func layoutBranchName(params WorkspaceLayoutParams) string {
	params = applyLayoutKey(params)
	raw := strings.TrimSpace(params.IssueIdentifier)
	if raw == "" {
		return "multica/" + sanitizeName(taskKey(params.TaskID))
	}
	if tbIdentKey(raw, identPrefixes(params)) != "" {
		return "feature-" + normalizeTBIdent(raw)
	}
	return "multica/" + sanitizeName(raw)
}

// childLayoutBranchName is the on_demand git_worktree branch. yaml
// branch.child_repos (e.g. feature-{tb_id}) wins when an implement id
// can be resolved from yaml ident prefixes; otherwise the root layout
// branch is reused.
func childLayoutBranchName(params WorkspaceLayoutParams, cfg workspaceLayoutFile) string {
	rootBranch := layoutBranchName(params)
	tmpl := strings.TrimSpace(cfg.Branch.ChildRepos)
	if tmpl == "" {
		return rootBranch
	}
	tb := implementTBIdent(params)
	if tb == "" {
		return rootBranch
	}
	out := strings.ReplaceAll(tmpl, "{tb_id}", tb)
	if out == "" || strings.Contains(out, "{") {
		return rootBranch
	}
	return out
}

// implementTBIdent is the {tb_id} for child_repos. Implement prefixes
// win over product prefixes so a spec mention in the same body does
// not become the implement branch. Prefixes come from yaml ident.*.
func implementTBIdent(params WorkspaceLayoutParams) string {
	idents := layoutIdentCandidates(params)
	params = applyLayoutKey(params)
	idents = append(idents, params.IssueIdentifier)
	impl := sanitizeIdentPrefixes(params.ImplementPrefixes)
	prod := sanitizeIdentPrefixes(params.ProductPrefixes)
	for _, raw := range idents {
		if k := tbIdentKey(raw, impl); k != "" {
			return k
		}
	}
	texts := append(append([]string{}, idents...), params.IssueTitle, params.IssueDescription)
	if k := firstIdentInText(identInTextRegexp(impl), texts...); k != "" {
		return k
	}
	for _, raw := range idents {
		if k := tbIdentKey(raw, prod); k != "" {
			return k
		}
	}
	return firstIdentInText(identInTextRegexp(prod), texts...)
}

func layoutIdentCandidates(params WorkspaceLayoutParams) []string {
	out := []string{params.IssueIdentifier, params.IssueParentIdentifier, params.LayoutOwnerIdentifier}
	for _, a := range params.LayoutAncestors {
		out = append(out, a.IssueIdentifier, a.ParentIdentifier)
	}
	return out
}

func firstIdentInText(re *regexp.Regexp, texts ...string) string {
	if re == nil {
		return ""
	}
	for _, text := range texts {
		if m := re.FindStringSubmatch(text); len(m) > 1 {
			return normalizeTBIdent(m[1])
		}
	}
	return ""
}

func normalizeTBIdent(raw string) string {
	parts := strings.Split(raw, "-")
	if len(parts) == 0 {
		return raw
	}
	parts[0] = strings.ToUpper(parts[0])
	return strings.Join(parts, "-")
}

// ParseLayoutRepoPaths extracts <prefix>/<name> mentions from issue text.
// pathPrefixes come from yaml; empty prefixes return nothing.
func ParseLayoutRepoPaths(pathPrefixes []string, texts ...string) []string {
	re := repoPathRegexp(pathPrefixes)
	if re == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, text := range texts {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			rel := normalizeRel(m[1])
			if rel == "." || seen[rel] {
				continue
			}
			seen[rel] = true
			out = append(out, rel)
		}
	}
	return out
}

func materialiseAlways(root, destRoot, branch string, cfg workspaceLayoutFile, wl *WorkspaceLayout, logger *slog.Logger) error {
	if len(cfg.Always) == 0 {
		if err := addMemberWorktree(root, destRoot, ".", branch, wl, logger); err != nil {
			return err
		}
		return linkIfExists(root, destRoot, "standard", "standard", wl)
	}
	for _, item := range cfg.Always {
		rel := normalizeRel(item.Path)
		switch strings.TrimSpace(item.Isolation) {
		case "git_worktree", "":
			if err := addMemberWorktree(root, destRoot, rel, branch, wl, logger); err != nil {
				return err
			}
		case "junction":
			// Semantic: shared read-through. Windows = mklink /J, Unix = symlink.
			src := item.Source
			if src == "" {
				src = rel
			}
			if err := linkIfExists(root, destRoot, normalizeRel(src), rel, wl); err != nil {
				return err
			}
		default:
			return fmt.Errorf("workspace_layout: isolation %q is not allowed (use git_worktree or junction)", item.Isolation)
		}
	}
	return nil
}

func materialiseOnDemand(root, destRoot, branch string, cfg workspaceLayoutFile, relevant []string, wl *WorkspaceLayout, logger *slog.Logger) error {
	seen := map[string]bool{}
	for _, m := range wl.Members {
		seen[m.RelPath] = true
	}
	include := strings.ToLower(strings.TrimSpace(cfg.OnDemand.GitWorktree.Include))
	var children []string
	if include == "task_relevant_only" {
		children = relevant
	} else {
		children = discoverChildGitRepos(root)
	}
	for _, rel := range children {
		rel = normalizeRel(rel)
		if rel == "." || seen[rel] {
			continue
		}
		if !isGitDir(filepath.Join(root, filepath.FromSlash(rel))) {
			continue
		}
		if err := addMemberWorktree(root, destRoot, rel, branch, wl, logger); err != nil {
			return err
		}
		seen[rel] = true
	}
	for _, pair := range cfg.OnDemand.GitWorktree.NestedMustWithParent {
		parent := normalizeRel(pair.Parent)
		child := normalizeRel(pair.Child)
		if !seen[parent] {
			continue
		}
		if seen[child] {
			continue
		}
		if !isGitDir(filepath.Join(root, filepath.FromSlash(child))) {
			continue
		}
		if err := addMemberWorktree(root, destRoot, child, branch, wl, logger); err != nil {
			return err
		}
		seen[child] = true
	}
	for _, j := range cfg.OnDemand.Junction {
		rel := normalizeRel(j.Path)
		src := j.Source
		if src == "" {
			src = rel
		}
		if j.When == "includes_frontend_core" && !seen["frontend/srm-front-core-op"] {
			continue
		}
		if err := linkIfExists(root, destRoot, normalizeRel(src), rel, wl); err != nil {
			return err
		}
	}
	return nil
}

func discoverChildGitRepos(root string) []string {
	var out []string
	for _, top := range []string{"backend", "frontend"} {
		dir := filepath.Join(root, top)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			rel := filepath.ToSlash(filepath.Join(top, e.Name()))
			if isGitDir(filepath.Join(root, filepath.FromSlash(rel))) {
				out = append(out, rel)
			}
		}
	}
	return out
}

func normalizeRel(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "." {
		return "."
	}
	return filepath.ToSlash(filepath.Clean(p))
}

func isGitDir(path string) bool {
	gitRoot, ok := detectGitRepo(path)
	if !ok {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return filepath.Clean(gitRoot) == filepath.Clean(abs)
}

func addMemberWorktree(root, destRoot, rel, branch string, wl *WorkspaceLayout, logger *slog.Logger) error {
	src := root
	if rel != "." {
		src = filepath.Join(root, filepath.FromSlash(rel))
	}
	if !isGitDir(src) {
		return fmt.Errorf("workspace_layout: %s is not an independent git repo", rel)
	}
	dest := destRoot
	if rel != "." {
		dest = filepath.Join(destRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("workspace_layout: mkdir %s: %w", dest, err)
		}
	}
	if worktreeOnBranch(dest, branch) {
		wl.Members = append(wl.Members, workspaceLayoutMember{
			RelPath: rel, SrcGit: src, DestPath: dest, Branch: branch, Kind: "git_worktree",
		})
		return nil
	}
	if rel == "." {
		if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
			if err := os.RemoveAll(dest); err != nil {
				return fmt.Errorf("workspace_layout: clear dest root: %w", err)
			}
		}
	} else if err := os.RemoveAll(dest); err != nil && !os.IsNotExist(err) {
		return err
	}
	unlock, err := lockGitRoot(src, logger)
	if err != nil {
		return fmt.Errorf("workspace_layout: lock %s: %w", rel, err)
	}
	createdBranch, addErr := attachLayoutWorktree(src, dest, branch, "HEAD")
	unlock()
	if addErr != nil {
		cleanupFailedWorktreeAdd(src, dest, branch, createdBranch)
		return fmt.Errorf("workspace_layout: worktree add %s: %w", rel, addErr)
	}
	wl.Members = append(wl.Members, workspaceLayoutMember{
		RelPath: rel, SrcGit: src, DestPath: dest, Branch: branch, Kind: "git_worktree",
		Created: true, CreatedBranch: createdBranch,
	})
	return nil
}

func linkIfExists(root, destRoot, srcRel, destRel string, wl *WorkspaceLayout) error {
	src := filepath.Join(root, filepath.FromSlash(srcRel))
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("workspace_layout: stat junction source %s: %w", srcRel, err)
	}
	dest := filepath.Join(destRoot, filepath.FromSlash(destRel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	already, err := sharedDirAlreadyLinked(dest, src)
	if err != nil {
		return err
	}
	if err := linkSharedDir(src, dest, destRoot); err != nil {
		return fmt.Errorf("workspace_layout: link %s -> %s: %w", destRel, srcRel, err)
	}
	wl.Members = append(wl.Members, workspaceLayoutMember{
		RelPath: destRel, SrcGit: src, DestPath: dest, Kind: "junction", Created: !already,
	})
	return nil
}

// Finalize leaves the composite tree mounted so the next run remounts or
// reuses the same dest. finalize_commit=leave keeps leftovers uncommitted
// so a project skill can draft the message; chore (default) snapshots them.
func (w *WorkspaceLayout) Finalize(logger *slog.Logger) (LocalWorktreeOutcome, error) {
	if w == nil {
		return LocalWorktreeOutcome{}, nil
	}
	outcome := LocalWorktreeOutcome{Branch: w.Branch}
	if normalizeFinalizeCommit(w.finalizeCommit) == finalizeCommitLeave {
		return outcome, nil
	}
	var firstErr error
	for i := len(w.Members) - 1; i >= 0; i-- {
		m := w.Members[i]
		if m.Kind != "git_worktree" {
			continue
		}
		if _, err := commitEverything(m.DestPath, "chore(agent): uncommitted changes from task", false); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("workspace_layout: commit %s: %w", m.RelPath, err)
		}
	}
	return outcome, firstErr
}

// Discard tears down every member. Tests and explicit cleanup use this.
func (w *WorkspaceLayout) Discard(logger *slog.Logger) {
	w.teardown(logger, true)
}

func (w *WorkspaceLayout) rollbackPrepare(logger *slog.Logger) {
	w.teardown(logger, false)
}

// teardown unregisters git worktrees. Shared mounts are unlinked first so a
// later RemoveAll of the dest cannot walk into the reference tree.
func (w *WorkspaceLayout) teardown(logger *slog.Logger, force bool) {
	if w == nil {
		return
	}
	for i := len(w.Members) - 1; i >= 0; i-- {
		m := w.Members[i]
		if !force && !m.Created {
			continue
		}
		if m.Kind != "git_worktree" {
			if m.Kind == "junction" {
				if err := unlinkSharedDir(m.DestPath); err != nil && logger != nil {
					logger.Warn("workspace_layout: unlink shared dir failed", "path", m.RelPath, "error", err)
				}
			}
			continue
		}
		unlock, err := lockGitRoot(m.SrcGit, logger)
		if err != nil {
			if logger != nil {
				logger.Warn("workspace_layout: lock for worktree remove failed", "path", m.RelPath, "error", err)
			}
			continue
		}
		if force {
			_ = removeLocalWorktreeDir(m.SrcGit, m.DestPath, logger)
		} else {
			cleanupFailedWorktreeAdd(m.SrcGit, m.DestPath, m.Branch, m.CreatedBranch)
		}
		unlock()
	}
}
