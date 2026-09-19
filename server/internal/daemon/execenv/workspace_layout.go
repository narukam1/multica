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
	// IssueParentID / IssueParentIdentifier are the issue row's parent
	// (not quick-create "file under"). Used to inherit dest/branch when
	// this step issue has no distinct TB identifier of its own.
	IssueParentID         string
	IssueParentIdentifier string
	// LayoutOwnerID / LayoutOwnerIdentifier are the resolved tree owner
	// (walked on the server). When set they win over one-hop parent inherit.
	LayoutOwnerID         string
	LayoutOwnerIdentifier string
	// RelevantRepos are backend/<name> / frontend/<name> paths this task
	// should materialise when the layout file says include: task_relevant_only.
	RelevantRepos []string
}

// LayoutKey is the dest/branch/lock identity for a workspace_layout tree.
type LayoutKey struct {
	IssueID         string
	IssueIdentifier string
}

// WorkspaceLayout is the prepared composite tree. Finalize commits leftovers
// onto the issue branch and leaves the tree mounted for the next run.
type WorkspaceLayout struct {
	WorkDir  string
	Members  []workspaceLayoutMember
	Branch   string
	prepared bool
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
	OnDemand struct {
		GitWorktree struct {
			Catalog              string `yaml:"catalog"`
			Include              string `yaml:"include"`
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
	destRoot := IssueLayoutWorkDir(params)
	if destRoot == "" {
		return nil, fmt.Errorf("workspace_layout: dest root is empty")
	}
	if err := os.MkdirAll(filepath.Dir(destRoot), 0o755); err != nil {
		return nil, fmt.Errorf("workspace_layout: create dest parent: %w", err)
	}
	branch := layoutBranchName(params)
	wl := &WorkspaceLayout{WorkDir: destRoot, Branch: branch}

	if err := materialiseAlways(root, destRoot, branch, cfg, wl, logger); err != nil {
		wl.rollbackPrepare(logger)
		return nil, err
	}
	if err := materialiseOnDemand(root, destRoot, branch, cfg, params.RelevantRepos, wl, logger); err != nil {
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

var tbIssueIdent = regexp.MustCompile(`(?i)^(lhwu|eegv|roiu|req)(-[0-9]+)+$`)

var layoutRepoPath = regexp.MustCompile(`(?i)\b((?:backend|frontend)/[A-Za-z0-9._-]+)`)

// ResolveLayoutKey picks the dest/branch owner for a workspace_layout run.
// A distinct TB identifier (LHWU/EEGV/ROIU/REQ) on this issue keeps its own
// tree; otherwise a step child inherits the parent. LayoutOwner*, when set
// by the server, is already walked and wins.
func ResolveLayoutKey(params WorkspaceLayoutParams) LayoutKey {
	childID := strings.TrimSpace(params.IssueID)
	childIdent := strings.TrimSpace(params.IssueIdentifier)
	if ownerID := strings.TrimSpace(params.LayoutOwnerID); ownerID != "" {
		ident := strings.TrimSpace(params.LayoutOwnerIdentifier)
		if ident == "" {
			ident = childIdent
		}
		return LayoutKey{IssueID: ownerID, IssueIdentifier: ident}
	}
	parentID := strings.TrimSpace(params.IssueParentID)
	if parentID == "" {
		return LayoutKey{IssueID: childID, IssueIdentifier: childIdent}
	}
	childTB := tbIdentKey(childIdent)
	parentTB := tbIdentKey(params.IssueParentIdentifier)
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
func WalkLayoutOwner(issueID, issueIdent string, lookup func(id string) (ident, parentID, parentIdent string, ok bool)) LayoutKey {
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
		key := ResolveLayoutKey(WorkspaceLayoutParams{
			IssueID:               currentID,
			IssueIdentifier:       currentIdent,
			IssueParentID:         parentID,
			IssueParentIdentifier: parentIdent,
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

func tbIdentKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if !tbIssueIdent.MatchString(raw) {
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
	if tbIssueIdent.MatchString(raw) {
		return "feature-" + normalizeTBIdent(raw)
	}
	return "multica/" + sanitizeName(raw)
}

func normalizeTBIdent(raw string) string {
	parts := strings.Split(raw, "-")
	if len(parts) == 0 {
		return raw
	}
	parts[0] = strings.ToUpper(parts[0])
	return strings.Join(parts, "-")
}

// ParseLayoutRepoPaths extracts backend/<name> and frontend/<name> mentions
// from issue/project text for task_relevant_only materialisation.
func ParseLayoutRepoPaths(texts ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, text := range texts {
		for _, m := range layoutRepoPath.FindAllStringSubmatch(text, -1) {
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

// Finalize commits leftovers onto the issue branch and leaves the composite
// tree mounted so the next run remounts or reuses the same dest.
func (w *WorkspaceLayout) Finalize(logger *slog.Logger) (LocalWorktreeOutcome, error) {
	if w == nil {
		return LocalWorktreeOutcome{}, nil
	}
	outcome := LocalWorktreeOutcome{Branch: w.Branch}
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
