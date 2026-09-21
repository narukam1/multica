package execenv

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	finalizeCommitChore  = "chore"
	finalizeCommitLeave  = "leave"
	projectBriefMaxBytes = 16 << 10
)

var workdirSkillRoots = []string{
	filepath.Join(".cursor", "skills"),
	filepath.Join(".agents", "skills"),
}

func normalizeFinalizeCommit(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case finalizeCommitLeave:
		return finalizeCommitLeave
	default:
		return finalizeCommitChore
	}
}

// applyWorkdirAgentConfig reads optional agent keys from
// .index/workspace-layout.yaml. It looks at cwd first, then the git
// common root (the reference tree when cwd is a worktree), so dest can
// pick up policy that has not been committed onto the dest branch.
// Missing yaml is a no-op.
func applyWorkdirAgentConfig(workDir string, ctx *TaskContextForEnv) {
	if ctx == nil || strings.TrimSpace(workDir) == "" {
		return
	}
	var (
		advertise   bool
		advertiseOn string
		briefRel    string
		briefRoot   string
	)
	for _, root := range layoutConfigRoots(workDir) {
		cfg, err := loadWorkspaceLayoutFile(root)
		if err != nil {
			continue
		}
		if cfg.Agent.AdvertiseWorkdirSkills {
			advertise = true
		}
		if on := strings.TrimSpace(cfg.Agent.AdvertiseOn); on != "" {
			advertiseOn = on
		}
		if rel := strings.TrimSpace(cfg.Agent.Brief); rel != "" {
			briefRel = rel
			briefRoot = root
		}
	}
	if !shouldApplyWorkdirAgentBrief(advertiseOn, workDir) {
		return
	}
	if advertise {
		ctx.WorkdirSkillNames = listWorkdirSkillNames(workDir)
	}
	if briefRel != "" {
		if body, ok := readLayoutRelativeFile(workDir, briefRel, projectBriefMaxBytes); ok {
			ctx.ProjectBrief = body
		} else if body, ok := readLayoutRelativeFile(briefRoot, briefRel, projectBriefMaxBytes); ok {
			ctx.ProjectBrief = body
		}
	}
}

const (
	advertiseOnAlways       = "always"
	advertiseOnDestWorktree = "dest_worktree"
)

func normalizeAdvertiseOn(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case advertiseOnAlways:
		return advertiseOnAlways
	default:
		return advertiseOnDestWorktree
	}
}

func shouldApplyWorkdirAgentBrief(advertiseOn, workDir string) bool {
	if normalizeAdvertiseOn(advertiseOn) == advertiseOnAlways {
		return true
	}
	return isDestWorktree(workDir)
}

func isDestWorktree(workDir string) bool {
	common := gitCommonRoot(workDir)
	if common == "" {
		return false
	}
	a, err1 := filepath.Abs(workDir)
	b, err2 := filepath.Abs(common)
	if err1 != nil || err2 != nil {
		return false
	}
	return filepath.Clean(a) != filepath.Clean(b)
}

func layoutConfigRoots(workDir string) []string {
	seen := map[string]struct{}{}
	var roots []string
	add := func(p string) {
		p = filepath.Clean(p)
		if p == "" || p == "." {
			return
		}
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		roots = append(roots, p)
	}
	add(workDir)
	if common := gitCommonRoot(workDir); common != "" {
		add(common)
	}
	return roots
}

func gitCommonRoot(workDir string) string {
	out, err := runGitTrimmed(workDir, "rev-parse", "--git-common-dir")
	if err != nil || strings.TrimSpace(out) == "" {
		return ""
	}
	common := strings.TrimSpace(out)
	if !filepath.IsAbs(common) {
		common = filepath.Join(workDir, common)
	}
	if filepath.Base(common) != ".git" {
		return ""
	}
	return filepath.Clean(filepath.Dir(common))
}

func listWorkdirSkillNames(workDir string) []string {
	seen := map[string]struct{}{}
	var names []string
	for _, rel := range workdirSkillRoots {
		entries, err := os.ReadDir(filepath.Join(workDir, rel))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := strings.TrimSpace(entry.Name())
			if name == "" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				continue
			}
			if _, err := os.Stat(filepath.Join(workDir, rel, name, "SKILL.md")); err != nil {
				continue
			}
			slug := sanitizeSkillName(name)
			if slug == "" {
				continue
			}
			if _, dup := seen[slug]; dup {
				continue
			}
			seen[slug] = struct{}{}
			names = append(names, slug)
		}
	}
	sort.Strings(names)
	return names
}

func readLayoutRelativeFile(workDir, rel string, maxBytes int) (string, bool) {
	clean, ok := layoutRelativePath(rel)
	if !ok {
		return "", false
	}
	full := filepath.Join(workDir, filepath.FromSlash(clean))
	if !pathWithinRoot(workDir, full) {
		return "", false
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return "", false
	}
	if maxBytes > 0 && len(raw) > maxBytes {
		raw = raw[:maxBytes]
	}
	body := strings.TrimRight(string(raw), " \t\r\n")
	if body == "" {
		return "", false
	}
	return body, true
}

func layoutRelativePath(rel string) (string, bool) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if filepath.IsAbs(rel) || strings.Contains(rel, ":") {
		return "", false
	}
	clean := pathCleanSlash(rel)
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", false
	}
	return clean, true
}

func pathCleanSlash(rel string) string {
	parts := strings.Split(rel, "/")
	var out []string
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		if p == ".." {
			if len(out) == 0 {
				return ".."
			}
			out = out[:len(out)-1]
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return "."
	}
	return strings.Join(out, "/")
}

func pathWithinRoot(root, candidate string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absCand, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absCand)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
