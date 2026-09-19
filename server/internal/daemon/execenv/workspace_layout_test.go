package execenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareWorkspaceLayoutBuildsNestedReposAndJunction(t *testing.T) {
	root := initCompositeReference(t)
	envRoot := t.TempDir()
	wl, err := PrepareWorkspaceLayout(WorkspaceLayoutParams{
		LocalPath:       root,
		EnvRoot:         envRoot,
		IssueIdentifier: "VEGA-9",
		TaskID:          "task-1",
	}, worktreeTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer wl.Discard(worktreeTestLogger())

	if _, err := os.Stat(filepath.Join(wl.WorkDir, "AGENTS.md")); err != nil {
		t.Fatalf("root file missing in composite tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wl.WorkDir, "backend", "svc-a", "main.go")); err != nil {
		t.Fatalf("child repo missing in composite tree: %v", err)
	}
	std := filepath.Join(wl.WorkDir, "standard", "marker.txt")
	if raw, err := os.ReadFile(std); err != nil {
		t.Fatalf("standard junction missing: %v", err)
	} else if string(raw) != "baseline\n" && string(raw) != "baseline\r\n" {
		t.Fatalf("standard content = %q", raw)
	}

	if err := os.WriteFile(filepath.Join(wl.WorkDir, "backend", "svc-a", "extra.go"), []byte("package svc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outcome, err := wl.Finalize(worktreeTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Branch != "multica/vega-9" {
		t.Fatalf("branch = %q", outcome.Branch)
	}
	child := filepath.Join(root, "backend", "svc-a")
	out, err := exec.Command("git", "-C", child, "rev-parse", "--verify", "multica/vega-9").CombinedOutput()
	if err != nil {
		t.Fatalf("child branch not delivered: %s: %v", out, err)
	}
}

func TestPrepareWorkspaceLayoutRequiresLayoutFile(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root, map[string]string{"README.md": "x\n"})
	_, err := PrepareWorkspaceLayout(WorkspaceLayoutParams{
		LocalPath: root,
		EnvRoot:   t.TempDir(),
		TaskID:    "t",
	}, worktreeTestLogger())
	if err == nil || !strings.Contains(err.Error(), "workspace-layout.yaml") {
		t.Fatalf("want missing layout error, got %v", err)
	}
}

func initCompositeReference(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	initGitRepo(t, root, map[string]string{"AGENTS.md": "# root\n"})
	if err := os.MkdirAll(filepath.Join(root, ".index"), 0o755); err != nil {
		t.Fatal(err)
	}
	layout := []byte(`
version: 1
kind: composite_workspace
always:
  - path: "."
    isolation: git_worktree
  - path: standard
    isolation: junction
    source: standard
on_demand:
  git_worktree:
    nested_must_with_parent: []
  junction: []
`)
	if err := os.WriteFile(filepath.Join(root, ".index", "workspace-layout.yaml"), layout, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "standard"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "standard", "marker.txt"), []byte("baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "backend", "svc-a")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, child, map[string]string{"main.go": "package svc\n"})
	return root
}

func initGitRepo(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	run("init")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-m", "init")
}
