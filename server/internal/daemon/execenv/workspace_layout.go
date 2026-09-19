package execenv

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const workspaceLayoutRelPath = ".index/workspace-layout.yaml"

// WorkspaceLayoutParams is the daemon-side request to materialise a composite
// working tree from a reference checkout (nested git repos + junctions).
type WorkspaceLayoutParams struct {
	LocalPath       string
	EnvRoot         string
	AgentName       string
	TaskID          string
	IssueIdentifier string
}

// WorkspaceLayout is the prepared composite tree. Finalize commits leftovers
// in each member worktree, then unregisters those worktrees. Branches stay.
type WorkspaceLayout struct {
	WorkDir  string
	Members  []workspaceLayoutMember
	Branch   string
	prepared bool
}

type workspaceLayoutMember struct {
	RelPath  string
	SrcGit   string
	DestPath string
	Branch   string
	Kind     string // git_worktree | junction
}

type workspaceLayoutFile struct {
	Always []struct {
		Path       string `yaml:"path"`
		Isolation  string `yaml:"isolation"`
		Source     string `yaml:"source"`
		Role       string `yaml:"role"`
	} `yaml:"always"`
	OnDemand struct {
		GitWorktree struct {
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
	destRoot := filepath.Join(params.EnvRoot, "workspace")
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return nil, fmt.Errorf("workspace_layout: create dest: %w", err)
	}
	branch := layoutBranchName(params)
	wl := &WorkspaceLayout{WorkDir: destRoot, Branch: branch}

	if err := materialiseAlways(root, destRoot, branch, cfg, wl, logger); err != nil {
		wl.Discard(logger)
		return nil, err
	}
	if err := materialiseOnDemand(root, destRoot, branch, cfg, wl, logger); err != nil {
		wl.Discard(logger)
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

func layoutBranchName(params WorkspaceLayoutParams) string {
	key := strings.TrimSpace(params.IssueIdentifier)
	if key == "" {
		key = taskKey(params.TaskID)
	}
	// sanitizeName lowercases first. Mapping before ToLower turned VEGA-5
	// into -----5 because uppercase Latin is not in [a-z0-9-_].
	return "multica/" + sanitizeName(key)
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

func materialiseOnDemand(root, destRoot, branch string, cfg workspaceLayoutFile, wl *WorkspaceLayout, logger *slog.Logger) error {
	seen := map[string]bool{}
	for _, m := range wl.Members {
		seen[m.RelPath] = true
	}
	for _, rel := range discoverChildGitRepos(root) {
		if seen[rel] {
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
	} else if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		// destRoot was MkdirAll'd; git worktree add wants to create it.
		if err := os.RemoveAll(dest); err != nil {
			return fmt.Errorf("workspace_layout: clear dest root: %w", err)
		}
	}
	if rel != "." {
		if err := os.RemoveAll(dest); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	unlock, err := lockGitRoot(src, logger)
	if err != nil {
		return fmt.Errorf("workspace_layout: lock %s: %w", rel, err)
	}
	addErr := runGitWorktreeAdd(src, dest, branch, "HEAD")
	unlock()
	if addErr != nil {
		cleanupFailedWorktreeAdd(src, dest, branch)
		return fmt.Errorf("workspace_layout: worktree add %s: %w", rel, addErr)
	}
	wl.Members = append(wl.Members, workspaceLayoutMember{
		RelPath: rel, SrcGit: src, DestPath: dest, Branch: branch, Kind: "git_worktree",
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
	if err := linkSharedDir(src, dest, destRoot); err != nil {
		return fmt.Errorf("workspace_layout: link %s -> %s: %w", destRel, srcRel, err)
	}
	wl.Members = append(wl.Members, workspaceLayoutMember{
		RelPath: destRel, SrcGit: src, DestPath: dest, Kind: "junction",
	})
	return nil
}

// Finalize commits dirty member worktrees, then unregisters them. Branches remain.
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
	if firstErr != nil {
		return outcome, firstErr
	}
	w.Discard(logger)
	return outcome, nil
}

// Discard unregisters git worktrees. Shared mounts are unlinked first so a
// later RemoveAll of the dest cannot walk into the reference tree.
func (w *WorkspaceLayout) Discard(logger *slog.Logger) {
	if w == nil {
		return
	}
	for i := len(w.Members) - 1; i >= 0; i-- {
		m := w.Members[i]
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
		_ = removeLocalWorktreeDir(m.SrcGit, m.DestPath, logger)
		unlock()
	}
}
