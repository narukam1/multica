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
	if _, err := os.Stat(filepath.Join(wl.WorkDir, "backend", "svc-a", "extra.go")); err != nil {
		t.Fatalf("finalize must keep dest mounted: %v", err)
	}
	child := filepath.Join(root, "backend", "svc-a")
	out, err := exec.Command("git", "-C", child, "rev-parse", "--verify", "multica/vega-9").CombinedOutput()
	if err != nil {
		t.Fatalf("child branch not delivered: %s: %v", out, err)
	}
}

func TestPrepareWorkspaceLayoutReusesMountedDest(t *testing.T) {
	root := initCompositeReference(t)
	envRoot := t.TempDir()
	params := WorkspaceLayoutParams{
		LocalPath: root, EnvRoot: envRoot, IssueIdentifier: "VEGA-9", TaskID: "task-1",
	}
	first, err := PrepareWorkspaceLayout(params, worktreeTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(first.WorkDir, "backend", "svc-a", "keep.go")
	if err := os.WriteFile(marker, []byte("package svc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Finalize(worktreeTestLogger()); err != nil {
		t.Fatal(err)
	}

	second, err := PrepareWorkspaceLayout(params, worktreeTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Discard(worktreeTestLogger())
	if _, err := os.Stat(filepath.Join(second.WorkDir, "backend", "svc-a", "keep.go")); err != nil {
		t.Fatalf("reuse lost gathered file: %v", err)
	}
}

func TestAttachLayoutWorktreeRemountsExistingBranch(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root, map[string]string{"README.md": "x\n"})
	first := filepath.Join(t.TempDir(), "wt1")
	created, err := attachLayoutWorktree(root, first, "multica/vega-5", "HEAD")
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	if err := os.WriteFile(filepath.Join(first, "keep.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := commitEverything(first, "keep", false); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "worktree", "remove", "--force", first).CombinedOutput(); err != nil {
		t.Fatalf("remove: %s: %v", out, err)
	}
	second := filepath.Join(t.TempDir(), "wt2")
	created, err = attachLayoutWorktree(root, second, "multica/vega-5", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("remount created a new branch")
	}
	raw, err := os.ReadFile(filepath.Join(second, "keep.txt"))
	if err != nil || strings.TrimSpace(string(raw)) != "keep" {
		t.Fatalf("remount content = %q err=%v", raw, err)
	}
}

func TestPrepareWorkspaceLayoutTaskRelevantOnly(t *testing.T) {
	root := initCompositeReference(t)
	other := filepath.Join(root, "backend", "svc-b")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, other, map[string]string{"other.go": "package other\n"})
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
    include: task_relevant_only
`)
	if err := os.WriteFile(filepath.Join(root, ".index", "workspace-layout.yaml"), layout, 0o644); err != nil {
		t.Fatal(err)
	}
	wl, err := PrepareWorkspaceLayout(WorkspaceLayoutParams{
		LocalPath: root, EnvRoot: t.TempDir(), IssueIdentifier: "VEGA-9", TaskID: "t",
		RelevantRepos: []string{"backend/svc-a"},
	}, worktreeTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer wl.Discard(worktreeTestLogger())
	if _, err := os.Stat(filepath.Join(wl.WorkDir, "backend", "svc-a", "main.go")); err != nil {
		t.Fatalf("relevant child missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wl.WorkDir, "backend", "svc-b", "other.go")); err == nil {
		t.Fatal("unrelated child was materialised")
	}
}

func TestLayoutBranchNameKeepsLatinIssueIdentifier(t *testing.T) {
	got := layoutBranchName(WorkspaceLayoutParams{IssueIdentifier: "VEGA-5", TaskID: "task-1"})
	if got != "multica/vega-5" {
		t.Fatalf("layoutBranchName = %q, want multica/vega-5", got)
	}
}

func TestLayoutBranchNameUsesFeatureForTBIdent(t *testing.T) {
	got := layoutBranchName(WorkspaceLayoutParams{IssueIdentifier: "LHWU-252", TaskID: "task-1"})
	if got != "feature-LHWU-252" {
		t.Fatalf("layoutBranchName = %q, want feature-LHWU-252", got)
	}
	if got := layoutBranchName(WorkspaceLayoutParams{IssueIdentifier: "lhwu-252"}); got != "feature-LHWU-252" {
		t.Fatalf("normalized = %q", got)
	}
}

func TestGitArgsEnableLongPaths(t *testing.T) {
	got := gitArgs("/repo", "worktree", "add", "-b", "b", "/dest", "HEAD")
	if len(got) < 2 || got[0] != "-c" || got[1] != gitLongPathsConfig {
		t.Fatalf("gitArgs = %q, want leading -c %s", got, gitLongPathsConfig)
	}
}

func TestIssueLayoutWorkDirIgnoresTaskID(t *testing.T) {
	a := IssueLayoutWorkDir(WorkspaceLayoutParams{
		WorkspacesRoot:  "/root",
		WorkspaceID:     "9ff372c4-ae7b-48f8-9433-34539dcdec38",
		WorkspaceSlug:   "vega-2b6i",
		IssueID:         "01a0ba16-7cc6-77c8-97ff-81b6076b4f0f",
		IssueIdentifier: "VEGA-5",
		TaskID:          "task-aaaa",
		EnvRoot:         "/root/ws/task-aaaa",
	})
	b := IssueLayoutWorkDir(WorkspaceLayoutParams{
		WorkspacesRoot:  "/root",
		WorkspaceID:     "9ff372c4-ae7b-48f8-9433-34539dcdec38",
		WorkspaceSlug:   "vega-2b6i",
		IssueID:         "01a0ba16-7cc6-77c8-97ff-81b6076b4f0f",
		IssueIdentifier: "VEGA-5",
		TaskID:          "task-bbbb",
		EnvRoot:         "/root/ws/task-bbbb",
	})
	if a != b {
		t.Fatalf("dest changed across tasks:\n%s\n%s", a, b)
	}
	if strings.Contains(a, "task-aaaa") || strings.Contains(a, "task-bbbb") {
		t.Fatalf("dest must not include task id: %s", a)
	}
}

func TestParseLayoutRepoPaths(t *testing.T) {
	got := ParseLayoutRepoPaths("改 backend/srm-source-op 与 frontend/srm-front-core-op")
	if len(got) != 2 || got[0] != "backend/srm-source-op" || got[1] != "frontend/srm-front-core-op" {
		t.Fatalf("ParseLayoutRepoPaths = %#v", got)
	}
}

func TestChildLayoutBranchNameUsesYamlTBTemplate(t *testing.T) {
	cfg := workspaceLayoutFile{}
	cfg.Branch.ChildRepos = "feature-{tb_id}"
	got := childLayoutBranchName(WorkspaceLayoutParams{
		IssueIdentifier:  "VEGA-11",
		IssueDescription: "关联 REQ-2609-198 / TB LHWU-287，实现 backend/srm-purchase-cooperation-op",
	}, cfg)
	if got != "feature-LHWU-287" {
		t.Fatalf("childLayoutBranchName = %q, want feature-LHWU-287", got)
	}
}

func TestChildLayoutBranchNameFallsBackWithoutTB(t *testing.T) {
	cfg := workspaceLayoutFile{}
	cfg.Branch.ChildRepos = "feature-{tb_id}"
	got := childLayoutBranchName(WorkspaceLayoutParams{IssueIdentifier: "VEGA-11"}, cfg)
	if got != "multica/vega-11" {
		t.Fatalf("childLayoutBranchName = %q, want multica/vega-11", got)
	}
}

func TestImplementTBIdentPrefersLHWUOverREQ(t *testing.T) {
	got := implementTBIdent(WorkspaceLayoutParams{
		IssueIdentifier:  "VEGA-11",
		IssueDescription: "产品 REQ-2609-198；开发 LHWU-287",
	})
	if got != "LHWU-287" {
		t.Fatalf("implementTBIdent = %q, want LHWU-287", got)
	}
}

func TestPrepareWorkspaceLayoutChildUsesFeatureTBBranch(t *testing.T) {
	root := initCompositeReference(t)
	other := filepath.Join(root, "backend", "svc-b")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, other, map[string]string{"other.go": "package other\n"})
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
    include: task_relevant_only
branch:
  child_repos: "feature-{tb_id}"
  root: inherit_or_task
`)
	if err := os.WriteFile(filepath.Join(root, ".index", "workspace-layout.yaml"), layout, 0o644); err != nil {
		t.Fatal(err)
	}
	desc := "实现 backend/svc-a，关联 REQ-2609-198 / LHWU-287"
	wl, err := PrepareWorkspaceLayout(WorkspaceLayoutParams{
		LocalPath:        root,
		EnvRoot:          t.TempDir(),
		IssueIdentifier:  "VEGA-11",
		IssueDescription: desc,
		TaskID:           "t",
		RelevantRepos:    ParseLayoutRepoPaths(desc),
	}, worktreeTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer wl.Discard(worktreeTestLogger())
	if wl.Branch != "multica/vega-11" {
		t.Fatalf("root branch = %q, want multica/vega-11", wl.Branch)
	}
	if _, err := os.Stat(filepath.Join(wl.WorkDir, "backend", "svc-a", "main.go")); err != nil {
		t.Fatalf("relevant child missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wl.WorkDir, "backend", "svc-b", "other.go")); err == nil {
		t.Fatal("unrelated child was materialised")
	}
	child := filepath.Join(root, "backend", "svc-a")
	out, err := exec.Command("git", "-C", child, "rev-parse", "--abbrev-ref", "feature-LHWU-287").CombinedOutput()
	if err != nil {
		t.Fatalf("child branch feature-LHWU-287 missing: %s: %v", out, err)
	}
	head, err := exec.Command("git", "-C", filepath.Join(wl.WorkDir, "backend", "svc-a"), "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(head)); got != "feature-LHWU-287" {
		t.Fatalf("child HEAD = %q, want feature-LHWU-287", got)
	}
}

func TestResolveLayoutKeyInheritsParentWithoutTB(t *testing.T) {
	got := ResolveLayoutKey(WorkspaceLayoutParams{
		IssueID:               "child",
		IssueIdentifier:       "VEGA-6",
		IssueParentID:         "parent",
		IssueParentIdentifier: "VEGA-5",
	})
	if got.IssueID != "parent" || got.IssueIdentifier != "VEGA-5" {
		t.Fatalf("got %+v, want parent/VEGA-5", got)
	}
}

func TestResolveLayoutKeyKeepsDistinctTBChild(t *testing.T) {
	got := ResolveLayoutKey(WorkspaceLayoutParams{
		IssueID:               "child",
		IssueIdentifier:       "LHWU-300",
		IssueParentID:         "parent",
		IssueParentIdentifier: "LHWU-252",
	})
	if got.IssueID != "child" || got.IssueIdentifier != "LHWU-300" {
		t.Fatalf("got %+v, want child/LHWU-300", got)
	}
}

func TestResolveLayoutKeyChildTBOwnsWhenParentHasNone(t *testing.T) {
	got := ResolveLayoutKey(WorkspaceLayoutParams{
		IssueID:               "child",
		IssueIdentifier:       "LHWU-252",
		IssueParentID:         "parent",
		IssueParentIdentifier: "VEGA-5",
	})
	if got.IssueID != "child" || got.IssueIdentifier != "LHWU-252" {
		t.Fatalf("got %+v, want child/LHWU-252", got)
	}
}

func TestResolveLayoutKeyPrefersLayoutOwner(t *testing.T) {
	got := ResolveLayoutKey(WorkspaceLayoutParams{
		IssueID:               "child",
		IssueIdentifier:       "VEGA-7",
		IssueParentID:         "mid",
		IssueParentIdentifier: "VEGA-6",
		LayoutOwnerID:         "root",
		LayoutOwnerIdentifier: "LHWU-252",
	})
	if got.IssueID != "root" || got.IssueIdentifier != "LHWU-252" {
		t.Fatalf("got %+v, want root/LHWU-252", got)
	}
}

func TestWalkLayoutOwnerClimbsToRoot(t *testing.T) {
	type node struct{ ident, parentID, parentIdent string }
	tree := map[string]node{
		"vega-7": {ident: "VEGA-7", parentID: "vega-6", parentIdent: "VEGA-6"},
		"vega-6": {ident: "VEGA-6", parentID: "vega-5", parentIdent: "VEGA-5"},
		"vega-5": {ident: "VEGA-5"},
	}
	got := WalkLayoutOwner("vega-7", "VEGA-7", func(id string) (ident, parentID, parentIdent string, ok bool) {
		n, ok := tree[id]
		return n.ident, n.parentID, n.parentIdent, ok
	})
	if got.IssueID != "vega-5" || got.IssueIdentifier != "VEGA-5" {
		t.Fatalf("got %+v, want vega-5/VEGA-5", got)
	}
}

func TestWalkLayoutOwnerStopsOnDistinctTB(t *testing.T) {
	type node struct{ ident, parentID, parentIdent string }
	tree := map[string]node{
		"child":  {ident: "LHWU-300", parentID: "parent", parentIdent: "LHWU-252"},
		"parent": {ident: "LHWU-252"},
	}
	got := WalkLayoutOwner("child", "LHWU-300", func(id string) (ident, parentID, parentIdent string, ok bool) {
		n, ok := tree[id]
		return n.ident, n.parentID, n.parentIdent, ok
	})
	if got.IssueID != "child" || got.IssueIdentifier != "LHWU-300" {
		t.Fatalf("got %+v, want child/LHWU-300", got)
	}
}

func TestIssueLayoutWorkDirInheritsParent(t *testing.T) {
	parent := IssueLayoutWorkDir(WorkspaceLayoutParams{
		WorkspacesRoot:  "/root",
		WorkspaceID:     "9ff372c4-ae7b-48f8-9433-34539dcdec38",
		WorkspaceSlug:   "vega-2b6i",
		IssueID:         "01a0ba16-7cc6-77c8-97ff-81b6076b4f0f",
		IssueIdentifier: "VEGA-5",
	})
	child := IssueLayoutWorkDir(WorkspaceLayoutParams{
		WorkspacesRoot:        "/root",
		WorkspaceID:           "9ff372c4-ae7b-48f8-9433-34539dcdec38",
		WorkspaceSlug:         "vega-2b6i",
		IssueID:               "01a0ba17-7cc6-77c8-97ff-81b6076b4f10",
		IssueIdentifier:       "VEGA-6",
		IssueParentID:         "01a0ba16-7cc6-77c8-97ff-81b6076b4f0f",
		IssueParentIdentifier: "VEGA-5",
	})
	if parent != child {
		t.Fatalf("child dest diverged:\nparent %s\nchild  %s", parent, child)
	}
}

func TestLayoutBranchNameInheritsParentTB(t *testing.T) {
	got := layoutBranchName(WorkspaceLayoutParams{
		IssueIdentifier:       "VEGA-6",
		IssueParentID:         "parent",
		IssueParentIdentifier: "LHWU-252",
	})
	if got != "feature-LHWU-252" {
		t.Fatalf("layoutBranchName = %q, want feature-LHWU-252", got)
	}
}

func TestPrepareWorkspaceLayoutChildReusesParentDest(t *testing.T) {
	root := initCompositeReference(t)
	wsRoot := t.TempDir()
	parentParams := WorkspaceLayoutParams{
		LocalPath:       root,
		WorkspacesRoot:  wsRoot,
		WorkspaceID:     "9ff372c4-ae7b-48f8-9433-34539dcdec38",
		WorkspaceSlug:   "vega-2b6i",
		IssueID:         "01a0ba16-7cc6-77c8-97ff-81b6076b4f0f",
		IssueIdentifier: "VEGA-5",
		TaskID:          "task-parent",
	}
	parent, err := PrepareWorkspaceLayout(parentParams, worktreeTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(parent.WorkDir, "backend", "svc-a", "keep.go")
	if err := os.WriteFile(marker, []byte("package svc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := parent.Finalize(worktreeTestLogger()); err != nil {
		t.Fatal(err)
	}

	child, err := PrepareWorkspaceLayout(WorkspaceLayoutParams{
		LocalPath:             root,
		WorkspacesRoot:        wsRoot,
		WorkspaceID:           "9ff372c4-ae7b-48f8-9433-34539dcdec38",
		WorkspaceSlug:         "vega-2b6i",
		IssueID:               "01a0ba17-7cc6-77c8-97ff-81b6076b4f10",
		IssueIdentifier:       "VEGA-6",
		IssueParentID:         parentParams.IssueID,
		IssueParentIdentifier: parentParams.IssueIdentifier,
		TaskID:                "task-child",
	}, worktreeTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer child.Discard(worktreeTestLogger())
	if child.WorkDir != parent.WorkDir {
		t.Fatalf("child dest %s != parent dest %s", child.WorkDir, parent.WorkDir)
	}
	if child.Branch != "multica/vega-5" {
		t.Fatalf("child branch = %q, want multica/vega-5", child.Branch)
	}
	if _, err := os.Stat(filepath.Join(child.WorkDir, "backend", "svc-a", "keep.go")); err != nil {
		t.Fatalf("child did not remount parent tree: %v", err)
	}
}

func TestCleanupFailedWorktreeAddPreservesExistingBranch(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root, map[string]string{"README.md": "x\n"})
	dest := filepath.Join(t.TempDir(), "wt")
	if err := runGitWorktreeAdd(root, dest, "multica/vega-5", "HEAD"); err != nil {
		t.Fatal(err)
	}
	cleanupFailedWorktreeAdd(root, dest, "multica/vega-5", false)
	if out, err := exec.Command("git", "-C", root, "rev-parse", "--verify", "multica/vega-5").CombinedOutput(); err != nil {
		t.Fatalf("issue branch was deleted: %s: %v", out, err)
	}
}

func TestCleanupFailedWorktreeAddRemovesOrphanBranch(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root, map[string]string{"README.md": "x\n"})
	dest := filepath.Join(t.TempDir(), "wt")
	if err := runGitWorktreeAdd(root, dest, "multica/vega-5", "HEAD"); err != nil {
		t.Fatal(err)
	}
	cleanupFailedWorktreeAdd(root, dest, "multica/vega-5", true)
	out, err := exec.Command("git", "-C", root, "rev-parse", "--verify", "multica/vega-5").CombinedOutput()
	if err == nil {
		t.Fatalf("orphan branch still present: %s", out)
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
