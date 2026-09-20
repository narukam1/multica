package daemon

import "testing"

func TestWorkspaceLayoutParamsReadIssueDescription(t *testing.T) {
	params := workspaceLayoutParamsForTask(Task{
		IssueIdentifier:  "VEGA-11",
		IssueTitle:       "P3 实现",
		IssueDescription: "实现 backend/srm-purchase-cooperation-op，关联 LHWU-287",
		ThreadName:       "P3 实现",
	}, "", "")
	if len(params.RelevantRepos) != 1 || params.RelevantRepos[0] != "backend/srm-purchase-cooperation-op" {
		t.Fatalf("RelevantRepos = %#v", params.RelevantRepos)
	}
	if params.IssueDescription != "实现 backend/srm-purchase-cooperation-op，关联 LHWU-287" {
		t.Fatalf("IssueDescription = %q", params.IssueDescription)
	}
}
