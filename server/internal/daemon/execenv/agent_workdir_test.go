package execenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestListWorkdirSkillNames(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSkill := func(rel string) {
		t.Helper()
		dir := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: x\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(".cursor/skills/tb-commit")
	writeSkill(".cursor/skills/tb-help")
	writeSkill(".agents/skills/tb-commit")
	writeSkill(".cursor/skills/_ignored")
	if err := os.WriteFile(filepath.Join(root, ".cursor", "skills", "not-a-skill.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := listWorkdirSkillNames(root)
	if len(got) != 2 || got[0] != "tb-commit" || got[1] != "tb-help" {
		t.Fatalf("listWorkdirSkillNames = %#v", got)
	}
}

func TestReadLayoutRelativeFileRejectsEscape(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readLayoutRelativeFile(root, "../ok.md", projectBriefMaxBytes); ok {
		t.Fatal("escaped path must be rejected")
	}
	if _, ok := readLayoutRelativeFile(root, "C:/windows/x.md", projectBriefMaxBytes); ok {
		t.Fatal("absolute path must be rejected")
	}
	body, ok := readLayoutRelativeFile(root, "ok.md", projectBriefMaxBytes)
	if !ok || body != "hello" {
		t.Fatalf("ok.md = %q ok=%v", body, ok)
	}
}

func TestWriteSkillsIncludesWorkdirNames(t *testing.T) {
	t.Parallel()
	out := buildMetaSkillContent("claude", TaskContextForEnv{
		IssueID:           "issue-1",
		AgentName:         "Eve",
		AgentID:           "eve-1",
		WorkdirSkillNames: []string{"tb-commit"},
		ProjectBrief:      "Prefer project skills in cwd.",
		AgentSkills: []SkillContextForEnv{{
			Name:    "PR Review",
			Content: "---\nname: pr-review\n---\n\nbody",
		}},
	})
	if !strings.Contains(out, "- **pr-review**\n") || !strings.Contains(out, "- **tb-commit**\n") {
		t.Fatalf("brief missing merged skill names:\n%s", out)
	}
	if !strings.Contains(out, "## Project conventions") || !strings.Contains(out, "Prefer project skills in cwd.") {
		t.Fatalf("brief missing project conventions:\n%s", out)
	}
	if !strings.Contains(out, "project skills") {
		t.Fatalf("brief missing workdir skill hint:\n%s", out)
	}
}

func TestInjectRuntimeConfigAdvertisesWorkdirSkills(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".index"), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := []byte(`
version: 1
kind: composite_workspace
agent:
  advertise_workdir_skills: true
  brief: .index/multica-brief.md
  finalize_commit: leave
always:
  - path: "."
    isolation: git_worktree
`)
	if err := os.WriteFile(filepath.Join(root, ".index", "workspace-layout.yaml"), yaml, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".index", "multica-brief.md"), []byte("Use cwd project skills.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(root, ".cursor", "skills", "tb-commit")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: tb-commit\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	content, err := InjectRuntimeConfig(root, "claude", TaskContextForEnv{
		IssueID:   "issue-1",
		AgentName: "Eve",
		AgentID:   "eve-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "- **tb-commit**\n") {
		t.Fatalf("injected brief missing workdir skill:\n%s", content)
	}
	if !strings.Contains(content, "Use cwd project skills.") {
		t.Fatalf("injected brief missing project brief:\n%s", content)
	}
}

func TestApplyWorkdirAgentConfigReadsGitCommonRoot(t *testing.T) {
	main := t.TempDir()
	initGitRepo(t, main, map[string]string{"README.md": "x\n"})
	if err := os.MkdirAll(filepath.Join(main, ".index"), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := []byte(`
version: 1
agent:
  advertise_workdir_skills: true
  brief: .index/multica-brief.md
always:
  - path: "."
    isolation: git_worktree
`)
	if err := os.WriteFile(filepath.Join(main, ".index", "workspace-layout.yaml"), yaml, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, ".index", "multica-brief.md"), []byte("from reference\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "dest")
	if out, err := exec.Command("git", "-C", main, "worktree", "add", dest, "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %s: %v", out, err)
	}
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", main, "worktree", "remove", "--force", dest).Run()
	})
	skillDir := filepath.Join(dest, ".cursor", "skills", "tb-help")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: tb-help\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var ctx TaskContextForEnv
	applyWorkdirAgentConfig(dest, &ctx)
	if len(ctx.WorkdirSkillNames) != 1 || ctx.WorkdirSkillNames[0] != "tb-help" {
		t.Fatalf("WorkdirSkillNames = %#v", ctx.WorkdirSkillNames)
	}
	if ctx.ProjectBrief != "from reference" {
		t.Fatalf("ProjectBrief = %q", ctx.ProjectBrief)
	}
}

func TestNormalizeFinalizeCommit(t *testing.T) {
	t.Parallel()
	if got := normalizeFinalizeCommit("leave"); got != finalizeCommitLeave {
		t.Fatalf("leave = %q", got)
	}
	if got := normalizeFinalizeCommit(""); got != finalizeCommitChore {
		t.Fatalf("default = %q", got)
	}
	if got := normalizeFinalizeCommit("CHORE"); got != finalizeCommitChore {
		t.Fatalf("chore = %q", got)
	}
}
