package workflows

import (
	"reflect"
	"testing"
)

func TestRepositoryReviewCampaignAuthorityIsScrubbedFromBrowserRunAndEvents(t *testing.T) {
	const canary = "rrc_campaign_privacy_canary"
	run := &Run{
		ID: "wr_campaign_privacy", WorkflowRef: RepositoryBugFinderWorkflowRef,
		Inputs:  map[string]any{"campaign_id": canary, "repository": "owner/repo"},
		Outputs: map[string]any{"run": map[string]any{"campaign_id": canary, "reviewed_files": 1}},
		Jobs: map[string]JobExecution{"find_bugs": {Outputs: map[string]any{
			"nested": []any{map[string]any{"campaignId": canary, "remaining_files": 0}},
		}}},
		Steps: map[string]StepExecution{"record": {Outputs: map[string]any{
			"run": map[string]any{"campaign_id": canary, "id": "saved"},
		}}},
	}
	projected := ProjectWorkflowRunForBrowser(run, false)
	if projected.Inputs["campaign_id"] != nil ||
		projected.Outputs["run"].(map[string]any)["campaign_id"] != nil ||
		projected.Jobs["find_bugs"].Outputs["nested"].([]any)[0].(map[string]any)["campaignId"] != nil ||
		projected.Steps["record"].Outputs["run"].(map[string]any)["campaign_id"] != nil {
		t.Fatalf("browser run exposed campaign authority: %#v", projected)
	}
	if run.Inputs["campaign_id"] != canary || run.Outputs["run"].(map[string]any)["campaign_id"] != canary ||
		run.Steps["record"].Outputs["run"].(map[string]any)["campaign_id"] != canary {
		t.Fatal("browser projection mutated stored campaign authority")
	}
	events := []RunEvent{{Payload: map[string]any{
		"outputs": map[string]any{"run": map[string]any{"campaign_id": canary, "remaining_files": 0}},
	}}}
	projectedEvents := ProjectRepositoryReviewRunEventsForBrowser(run, events, false, false)
	if projectedEvents[0].Payload["outputs"].(map[string]any)["run"].(map[string]any)["campaign_id"] != nil {
		t.Fatalf("browser event exposed campaign authority: %#v", projectedEvents)
	}
	if !reflect.DeepEqual(
		events[0].Payload["outputs"].(map[string]any)["run"].(map[string]any)["campaign_id"],
		canary,
	) {
		t.Fatal("event projection mutated stored payload")
	}
}

func TestRepositoryReviewCampaignScrubbingDoesNotAffectOtherWorkflows(t *testing.T) {
	run := &Run{WorkflowRef: "workflows/other.yml", Inputs: map[string]any{"campaign_id": "ordinary-data"}}
	projected := ProjectWorkflowRunForBrowser(run, false)
	if projected.Inputs["campaign_id"] != "ordinary-data" {
		t.Fatalf("unrelated workflow campaign-like data was scrubbed: %#v", projected.Inputs)
	}
}
