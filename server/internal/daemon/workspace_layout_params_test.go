package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceLayoutParamsReadIssueDescription(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".index"), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := []byte(`
version: 1
kind: composite_workspace
ident:
  implement_prefixes: [LHWU, EEGV, ROIU]
  product_prefixes: [REQ]
on_demand:
  git_worktree:
    path_prefixes: [backend, frontend]
always:
  - path: "."
    isolation: git_worktree
`)
	if err := os.WriteFile(filepath.Join(root, ".index", "workspace-layout.yaml"), yaml, 0o644); err != nil {
		t.Fatal(err)
	}
	params := workspaceLayoutParamsForTask(Task{
		IssueIdentifier:  "VEGA-11",
		IssueTitle:       "P3 实现",
		IssueDescription: "实现 backend/srm-purchase-cooperation-op，关联 LHWU-287",
		ThreadName:       "P3 实现",
	}, "", root)
	if len(params.RelevantRepos) != 1 || params.RelevantRepos[0] != "backend/srm-purchase-cooperation-op" {
		t.Fatalf("RelevantRepos = %#v", params.RelevantRepos)
	}
	if params.IssueDescription != "实现 backend/srm-purchase-cooperation-op，关联 LHWU-287" {
		t.Fatalf("IssueDescription = %q", params.IssueDescription)
	}
	if len(params.ImplementPrefixes) != 3 || params.ImplementPrefixes[0] != "LHWU" {
		t.Fatalf("ImplementPrefixes = %#v", params.ImplementPrefixes)
	}
}

func TestWorkspaceLayoutParamsWithoutYamlGuessNothing(t *testing.T) {
	params := workspaceLayoutParamsForTask(Task{
		IssueIdentifier:  "VEGA-11",
		IssueDescription: "实现 backend/srm-purchase-cooperation-op，关联 LHWU-287",
	}, "", "")
	if len(params.RelevantRepos) != 0 {
		t.Fatalf("RelevantRepos = %#v, want empty without yaml prefixes", params.RelevantRepos)
	}
	if len(params.ImplementPrefixes) != 0 {
		t.Fatalf("ImplementPrefixes = %#v, want empty without yaml", params.ImplementPrefixes)
	}
}
