package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestHydrateImmutableGitScopeCoversBoundedEvidenceOutcomes(t *testing.T) {
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	contents := map[string]string{
		"plain.go":   "package plain\n",
		"second.go":  "package second\n",
		"binary.dat": "visible\x00binary",
		"escaped.go": strings.Repeat(`"\\`, 8),
	}
	for name, content := range contents {
		writeTestFile(t, filepath.Join(repo, name), content)
	}
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.email", "test@example.com")
	gitCmd(t, repo, "config", "user.name", "Test User")
	gitCmd(t, repo, "add", ".")
	gitCmd(t, repo, "commit", "-m", "immutable fixtures")

	inventory, inventoryErr := nativeCollectInventory(context.Background(), repo, "HEAD")
	if inventoryErr != nil {
		t.Fatal(inventoryErr)
	}
	byPath := make(map[string]nativeGitFile, len(inventory))
	for _, file := range inventory {
		byPath[file.Path] = file
	}
	exec := ExecutionContext{WorkspaceDir: workspace}
	ref := func(name string) map[string]any {
		file := byPath[name]
		return map[string]any{
			"path": name, "fileHash": file.BlobHash, "sizeBytes": file.SizeBytes,
			"category": "code", "source": map[string]any{"workspacePath": repo},
		}
	}

	if _, err := hydrateImmutableGitScope(
		context.Background(), []any{"not-an-object"}, nil, exec,
	); err == nil || !strings.Contains(err.Error(), "must be an object") {
		t.Fatalf("non-object scope error = %v", err)
	}
	binaryCategory, hydrateErr := hydrateImmutableGitScope(context.Background(), []any{map[string]any{
		"path": "image.png", "category": "binary",
	}}, nil, exec)
	if hydrateErr != nil {
		t.Fatal(hydrateErr)
	}
	binaryCategoryFile := binaryCategory.([]any)[0].(map[string]any)
	if binaryCategoryFile["contentComplete"] != false || binaryCategoryFile["contentUnavailable"] != "binary" {
		t.Fatalf("binary category hydration = %#v", binaryCategoryFile)
	}
	if _, err := hydrateImmutableGitScope(context.Background(), []any{map[string]any{
		"path": "missing.go", "category": "code", "sizeBytes": 1,
	}}, nil, exec); err == nil || !strings.Contains(err.Error(), "no workspace source") {
		t.Fatalf("missing source error = %v", err)
	}
	outside := t.TempDir()
	if _, err := hydrateImmutableGitScope(context.Background(), []any{map[string]any{
		"path": "escape.go", "category": "code", "sizeBytes": 1,
		"source": map[string]any{"workspacePath": outside},
	}}, nil, exec); err == nil || !strings.Contains(err.Error(), "stay inside") {
		t.Fatalf("escaping source error = %v", err)
	}
	negative := ref("plain.go")
	negative["sizeBytes"] = -1
	if _, err := hydrateImmutableGitScope(context.Background(), []any{negative}, nil, exec); err == nil ||
		!strings.Contains(err.Error(), "invalid size") {
		t.Fatalf("negative size error = %v", err)
	}

	tooLarge := ref("plain.go")
	tooLarge["sizeBytes"] = int64(32)
	result, hydrateErr := hydrateImmutableGitScope(
		context.Background(), []any{tooLarge}, map[string]any{"max_content_bytes": 8}, exec,
	)
	if hydrateErr != nil {
		t.Fatal(hydrateErr)
	}
	if got := result.([]any)[0].(map[string]any)["contentUnavailable"]; got != "file_too_large" {
		t.Fatalf("oversized metadata outcome = %q", got)
	}

	plain := ref("plain.go")
	second := ref("second.go")
	aggregateLimit := int(byPath["plain.go"].SizeBytes)
	result, hydrateErr = hydrateImmutableGitScope(
		context.Background(), []any{plain, second},
		map[string]any{"max_content_bytes": 128, "max_total_content_bytes": aggregateLimit}, exec,
	)
	if hydrateErr != nil {
		t.Fatal(hydrateErr)
	}
	aggregateFiles := result.([]any)
	if aggregateFiles[0].(map[string]any)["contentComplete"] != true ||
		aggregateFiles[1].(map[string]any)["contentUnavailable"] != "aggregate_limit" {
		t.Fatalf("aggregate hydration = %#v", aggregateFiles)
	}

	unreadable := ref("plain.go")
	unreadable["fileHash"] = strings.Repeat("f", 40)
	if _, err := hydrateImmutableGitScope(
		context.Background(), []any{unreadable}, map[string]any{"max_content_bytes": 128}, exec,
	); err == nil || !strings.Contains(err.Error(), "read immutable blob") {
		t.Fatalf("unreadable blob error = %v", err)
	}
	mismatched := ref("plain.go")
	mismatched["sizeBytes"] = byPath["plain.go"].SizeBytes + 1
	if _, err := hydrateImmutableGitScope(
		context.Background(), []any{mismatched}, map[string]any{"max_content_bytes": 128}, exec,
	); err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("blob size mismatch error = %v", err)
	}

	result, hydrateErr = hydrateImmutableGitScope(
		context.Background(), []any{ref("binary.dat")}, map[string]any{"max_content_bytes": 128}, exec,
	)
	if hydrateErr != nil || result.([]any)[0].(map[string]any)["contentUnavailable"] != "binary" {
		t.Fatalf("binary blob hydration = (%#v, %v)", result, hydrateErr)
	}
	escaped := ref("escaped.go")
	escapedLimit := int(byPath["escaped.go"].SizeBytes)
	result, hydrateErr = hydrateImmutableGitScope(
		context.Background(), []any{escaped}, map[string]any{"max_content_bytes": escapedLimit}, exec,
	)
	if hydrateErr != nil || result.([]any)[0].(map[string]any)["contentUnavailable"] != "file_too_large" {
		t.Fatalf("prompt-escaped blob hydration = (%#v, %v)", result, hydrateErr)
	}

	wrapper := map[string]any{"batch": "one", "items": []any{ref("plain.go")}}
	wrapped, hydrateErr := hydrateImmutableGitScope(
		context.Background(), wrapper, map[string]any{"max_content_bytes": 128}, exec,
	)
	if hydrateErr != nil || wrapped.(map[string]any)["batch"] != "one" ||
		len(wrapped.(map[string]any)["items"].([]any)) != 1 {
		t.Fatalf("wrapped hydration = (%#v, %v)", wrapped, hydrateErr)
	}
	empty, hydrateErr := hydrateImmutableGitScope(context.Background(), []any{}, nil, exec)
	if hydrateErr != nil || len(empty.([]any)) != 0 {
		t.Fatalf("empty any-slice hydration = (%#v, %v)", empty, hydrateErr)
	}
	if nativeReviewText("valid\x00text") || nativeReviewText(string([]byte{0xff})) {
		t.Fatal("NUL or invalid UTF-8 review text was accepted")
	}
}

func TestRepositoryReviewNativeCloseoutBranches(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "a.go"), "package review\n")
	writeTestFile(t, filepath.Join(workspace, "b.go"), "package review\n")
	gitCmd(t, workspace, "init")
	gitCmd(t, workspace, "config", "user.email", "test@example.com")
	gitCmd(t, workspace, "config", "user.name", "Test User")
	gitCmd(t, workspace, "remote", "add", "origin", "git@github.com:Owner/Repo.git")
	gitCmd(t, workspace, "add", ".")
	gitCmd(t, workspace, "commit", "-m", "review fixture")
	exec := ExecutionContext{
		WorkspaceDir: workspace, WorkflowRef: RepositoryBugFinderWorkflowRef, RunID: "closeout-review",
	}
	inventory, inventoryErr := nativeCollectInventory(context.Background(), workspace, "HEAD")
	if inventoryErr != nil {
		t.Fatal(inventoryErr)
	}
	inventoryHash, hashErr := nativeStableHash(inventory)
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	files := make([]map[string]any, 0, len(inventory))
	for _, file := range inventory {
		files = append(files, map[string]any{
			"path": file.Path, "fileHash": file.BlobHash, "sizeBytes": file.SizeBytes,
			"category": "code", "mode": file.Mode,
		})
	}
	basePlan := map[string]any{
		"action": "plan", "working_directory": ".", "commit": "HEAD",
		"inventory_hash": inventoryHash, "files": files,
	}
	badHash := cloneMap(basePlan)
	badHash["inventory_hash"] = "wrong"
	if _, err := nativeRepositoryReview(context.Background(), badHash, exec); err == nil ||
		!strings.Contains(err.Error(), "inventory hash") {
		t.Fatalf("plan binding error = %v", err)
	}
	badProfile := cloneMap(basePlan)
	badProfile["profile"] = make(chan int)
	if _, err := nativeRepositoryReview(context.Background(), badProfile, exec); err == nil ||
		!strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("profile hashing error = %v", err)
	}
	badIdentity := cloneMap(basePlan)
	badIdentity["repository"] = "different/repo"
	if _, err := nativeRepositoryReview(context.Background(), badIdentity, exec); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("repository identity error = %v", err)
	}

	planArgs := cloneMap(basePlan)
	planArgs["repository"] = "auto"
	planArgs["include_default_reviewer"] = true
	planArgs["resolved_reviewer_models"] = []any{"review-a", "review-b"}
	planArgs["max_files"] = 128
	planned, planErr := nativeRepositoryReview(context.Background(), planArgs, exec)
	if planErr != nil || planned["includeDefaultReviewer"] != true || planned["maxFiles"].(int) >= 128 {
		t.Fatalf("bounded default-reviewer plan = (%#v, %v)", planned, planErr)
	}

	if _, err := nativeRepositoryReview(context.Background(), map[string]any{
		"action": "freeze", "files": []any{map[string]any{"path": "image.png", "category": "binary"}},
	}, ExecutionContext{WorkspaceDir: workspace}); err == nil || !strings.Contains(err.Error(), "run identity") {
		t.Fatalf("freeze identity error = %v", err)
	}
	if _, err := nativeRepositoryReview(context.Background(), map[string]any{
		"action": "record", "plan": repoaudit.Plan{},
		"review": map[string]any{"summary": "empty", "reviewedFiles": []any{}, "findings": []any{}},
		"scope":  []map[string]any{},
	}, exec); err == nil {
		t.Fatal("record accepted a plan without durable identity")
	}
	if _, err := nativeRepositoryReview(context.Background(), map[string]any{
		"action": "result", "plan": repoaudit.Plan{Authoritative: true},
	}, exec); err == nil {
		t.Fatal("result finalized an authoritative plan without durable identity")
	}

	if files := nativeRepositoryReviewUnsupportedFiles([]map[string]any{{"scope": "bad"}}); len(files) != 0 {
		t.Fatalf("malformed child unsupported scope = %#v", files)
	}
	if files := nativeRepositoryReviewUnsupportedScopeFiles([]map[string]any{{
		"path": "later.go", "fileHash": "a", "sizeBytes": 1, "contentUnavailable": "aggregate_limit",
	}}); len(files) != 0 {
		t.Fatalf("transient unsupported scope = %#v", files)
	}
	if identity, err := nativeRepositoryReviewIdentity(
		context.Background(), map[string]any{"repository": "auto"}, exec,
	); err != nil || identity != "owner/repo" {
		t.Fatalf("auto repository identity = (%q, %v)", identity, err)
	}
	if got := nativeRepositorySourceIdentity(
		"relative/mirror.git",
		workspace,
	); got != filepath.Join(
		workspace,
		"relative/mirror.git",
	) {
		t.Fatalf("relative repository identity = %q", got)
	}
	workspaceMap := (nativeGitWorkspaceRef{
		ID: "workspace", RepoID: "owner/repo", RemoteURL: "remote", UpstreamURL: "upstream",
		Ref: "main", Path: workspace,
	}).Map()
	if workspaceMap["upstream_url"] != "upstream" || len(workspaceMap) != 6 {
		t.Fatalf("workspace map = %#v", workspaceMap)
	}
}

func TestRepositoryReviewObservationCloseoutBranches(t *testing.T) {
	refs := []repoaudit.FileRef{
		{Path: "b.go", BlobSHA: strings.Repeat("b", 40), SizeBytes: 2},
		{Path: "a.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 1},
	}
	scope := nativeRepositoryReviewFileMaps(refs)
	for _, file := range scope {
		file["contentComplete"] = true
	}
	valid := map[string]any{
		"summary": "reviewed", "reviewedFiles": []any{"b.go", "a.go"}, "findings": []any{},
	}
	observations, completed, err := nativeRepositoryReviewObservations(map[string]any{
		"managed_children": []map[string]any{
			{
				"valid": true, "required": true, "scope": scope,
				"model": map[string]any{"default": "fallback-reviewer"}, "structured": valid,
			},
			{
				"valid": true, "required": false, "scope": scope,
				"structured": map[string]any{"summary": "missing acknowledgement", "findings": []any{}},
			},
		},
	}, repoaudit.Plan{})
	if err != nil || len(observations) != 1 || observations[0].Model != "fallback-reviewer" ||
		!reflect.DeepEqual(completed, []repoaudit.FileRef{refs[1], refs[0]}) {
		t.Fatalf("managed observations = (%#v, %#v, %v)", observations, completed, err)
	}

	invalidFinding := map[string]any{
		"summary": "invalid", "reviewedFiles": []any{"a.go"},
		"findings": []any{map[string]any{"severity": map[string]any{"invalid": true}, "file": "a.go"}},
	}
	if _, _, err := nativeRepositoryReviewObservations(map[string]any{
		"managed_children": []map[string]any{{
			"valid": true, "scope": []map[string]any{scope[1]}, "structured": invalidFinding,
		}},
	}, repoaudit.Plan{}); err == nil || !strings.Contains(err.Error(), "managed child 0") {
		t.Fatalf("managed invalid finding error = %v", err)
	}
	if _, _, err := nativeRepositoryReviewObservations(map[string]any{
		"review": valid, "scope": "bad",
	}, repoaudit.Plan{}); err == nil {
		t.Fatal("single review accepted malformed scope")
	}
	if _, _, err := nativeRepositoryReviewObservations(map[string]any{
		"review": map[string]any{"reviewedFiles": []any{"outside.go"}, "findings": []any{}},
		"scope":  []map[string]any{scope[1]},
	}, repoaudit.Plan{}); err == nil || !strings.Contains(err.Error(), "not readable") {
		t.Fatalf("single review acknowledgement error = %v", err)
	}
	if _, _, err := nativeRepositoryReviewObservations(map[string]any{
		"review": invalidFinding, "scope": []map[string]any{scope[1]},
	}, repoaudit.Plan{}); err == nil {
		t.Fatal("single review accepted an invalid finding shape")
	}
}

func TestWorkflowCoverageOffsetsExercisePublicRuntimeContracts(t *testing.T) {
	contract := &AgentOutputContract{Format: "json", Schema: map[string]any{
		"type": "object", "properties": map[string]any{"summary": map[string]any{"type": "string"}},
	}}
	instruction := contract.Instruction()
	if !strings.Contains(instruction, "Return only valid JSON") || !strings.Contains(instruction, `"summary"`) {
		t.Fatalf("structured instruction = %q", instruction)
	}
	if got := (*AgentOutputContract)(nil).Instruction(); got != "" {
		t.Fatalf("nil structured instruction = %q", got)
	}
	badSchema := &AgentOutputContract{Format: "json", Schema: map[string]any{"bad": make(chan int)}}
	if got := badSchema.Instruction(); !strings.Contains(got, "Structured output contract") {
		t.Fatalf("unserializable-schema instruction = %q", got)
	}
	for _, test := range []struct {
		value any
		want  int
	}{
		{value: nil, want: 0},
		{value: "", want: 0},
		{value: "12345", want: 2},
		{value: map[string]any{"a": "b"}, want: 3},
	} {
		if got := EstimateAgentPayloadTokens(test.value); got != test.want {
			t.Fatalf("EstimateAgentPayloadTokens(%#v) = %d, want %d", test.value, got, test.want)
		}
	}
	if got := EstimateAgentPayloadTokens(make(chan int)); got == 0 {
		t.Fatal("fallback payload token estimate was empty")
	}

	registry := NewFunctionRegistry()
	if err := registry.Register(" ", func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		return nil, nil
	}); err == nil {
		t.Fatal("blank workflow function name was accepted")
	}
	if err := registry.Register("named", nil); err == nil {
		t.Fatal("nil workflow function was accepted")
	}
	if err := registry.Register("named", func(
		_ context.Context, args map[string]any, _ ExecutionContext,
	) (map[string]any, error) {
		return map[string]any{"value": args["value"]}, nil
	}); err != nil {
		t.Fatal(err)
	}
	output, err := registry.RunFunction(
		context.Background(), "named", map[string]any{"value": "ok"}, ExecutionContext{},
	)
	if err != nil || output["value"] != "ok" {
		t.Fatalf("registered workflow function = (%#v, %v)", output, err)
	}
	if _, err := registry.RunFunction(context.Background(), "missing", nil, ExecutionContext{}); err == nil {
		t.Fatal("missing workflow function was executed")
	}
	var nilRegistry *FunctionRegistry
	if _, err := nilRegistry.RunFunction(context.Background(), "named", nil, ExecutionContext{}); err == nil {
		t.Fatal("nil workflow function registry was executed")
	}

	if _, err := ValidateGateFieldValues([]GateField{{
		ID: "approved", Type: GateFieldBoolean, Required: true,
	}}, map[string]any{"approved": true}); err != nil {
		t.Fatalf("public gate field validation failed: %v", err)
	}
	if (workflowWaitingError{}).Error() != "workflow is waiting for human input" {
		t.Fatal("workflow waiting error text changed")
	}
}

func TestReloadLocalReportsLoadAndValidationFailures(t *testing.T) {
	workspace := t.TempDir()
	definitions := filepath.Join(workspace, DefaultDefinitionsDir)
	if err := os.MkdirAll(definitions, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(definitions, "valid.yml"), `
name: Valid
on:
  manual: {}
jobs:
  run:
    runs-on: picoclaw
    steps:
      - uses: function/workflow.state
        with:
          action: list
`)
	writeTestFile(t, filepath.Join(definitions, "broken-yaml.yml"), "name: [\n")
	writeTestFile(t, filepath.Join(definitions, "invalid.yml"), `
name: Invalid
on:
  manual: {}
jobs: {}
`)
	result, reloadErr := ReloadLocal(context.Background(), workspace)
	if reloadErr != nil {
		t.Fatal(reloadErr)
	}
	if len(result.Workflows) != 3 || len(result.Errors) != 2 || result.ReloadedAt.IsZero() {
		t.Fatalf("reload result = %#v", result)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReloadLocal(canceled, workspace); err == nil {
		t.Fatal("canceled workflow reload succeeded")
	}

	if _, err := InstallWorkflowTemplate(
		context.Background(), workspace, "unknown-template", false,
	); err == nil || !strings.Contains(err.Error(), "unknown workflow template") {
		t.Fatalf("unknown template error = %v", err)
	}
	installed, installErr := InstallWorkflowTemplate(
		context.Background(), workspace, RepositoryBugFinderWorkflowName, false,
	)
	if installErr != nil || installed == nil || !installed.Installed {
		t.Fatalf("generic template install = (%#v, %v)", installed, installErr)
	}
}

func TestWorkflowAuthoringScalarRuntimeContracts(t *testing.T) {
	marshalCases := []struct {
		name  string
		value WorkflowAuthoringScalar
		want  string
	}{
		{name: "null", value: WorkflowAuthoringScalar{kind: workflowAuthoringScalarNull}, want: "null"},
		{
			name:  "string",
			value: WorkflowAuthoringScalar{kind: workflowAuthoringScalarString, text: "safe"},
			want:  `"safe"`,
		},
		{
			name:  "number",
			value: WorkflowAuthoringScalar{kind: workflowAuthoringScalarNumber, number: "12.5"},
			want:  "12.5",
		},
		{
			name:  "unsafe number",
			value: WorkflowAuthoringScalar{kind: workflowAuthoringScalarNumber, number: "not-a-number"},
			want:  "null",
		},
		{
			name:  "boolean",
			value: WorkflowAuthoringScalar{kind: workflowAuthoringScalarBoolean, boolean: true},
			want:  "true",
		},
		{name: "unknown", value: WorkflowAuthoringScalar{kind: workflowAuthoringScalarKind(99)}, want: "null"},
	}
	for _, test := range marshalCases {
		t.Run(test.name, func(t *testing.T) {
			encoded, marshalErr := test.value.MarshalJSON()
			if marshalErr != nil || string(encoded) != test.want {
				t.Fatalf("MarshalJSON() = (%s, %v), want %s", encoded, marshalErr, test.want)
			}
		})
	}

	if unmarshalErr := (*WorkflowAuthoringScalar)(nil).UnmarshalJSON([]byte("null")); unmarshalErr == nil {
		t.Fatal("nil workflow authoring scalar destination was accepted")
	}
	unmarshalCases := []struct {
		name string
		raw  string
		kind workflowAuthoringScalarKind
	}{
		{name: "null", raw: "null", kind: workflowAuthoringScalarNull},
		{name: "true", raw: "true", kind: workflowAuthoringScalarBoolean},
		{name: "false", raw: "false", kind: workflowAuthoringScalarBoolean},
		{name: "string", raw: `"safe"`, kind: workflowAuthoringScalarString},
		{name: "number", raw: "42.5", kind: workflowAuthoringScalarNumber},
	}
	for _, test := range unmarshalCases {
		t.Run(test.name, func(t *testing.T) {
			var value WorkflowAuthoringScalar
			if unmarshalErr := value.UnmarshalJSON([]byte(test.raw)); unmarshalErr != nil || value.kind != test.kind {
				t.Fatalf("UnmarshalJSON(%s) = (%#v, %v)", test.raw, value, unmarshalErr)
			}
		})
	}
	for _, raw := range [][]byte{[]byte(`"unterminated`), []byte("not-a-number")} {
		var value WorkflowAuthoringScalar
		if unmarshalErr := value.UnmarshalJSON(raw); unmarshalErr == nil {
			t.Fatalf("invalid authoring scalar %q was accepted as %#v", raw, value)
		}
	}

	validScalars := []WorkflowAuthoringScalar{
		{kind: workflowAuthoringScalarNull},
		{kind: workflowAuthoringScalarBoolean, boolean: true},
		{kind: workflowAuthoringScalarString, text: "safe"},
		{kind: workflowAuthoringScalarNumber, number: "1"},
	}
	for _, value := range validScalars {
		if !validWorkflowAuthoringScalar(value) {
			t.Fatalf("valid authoring scalar was rejected: %#v", value)
		}
	}
	for _, value := range []WorkflowAuthoringScalar{
		{kind: workflowAuthoringScalarString, text: "bad\x00text"},
		{kind: workflowAuthoringScalarNumber, number: "not-a-number"},
		{kind: workflowAuthoringScalarKind(99)},
	} {
		if validWorkflowAuthoringScalar(value) {
			t.Fatalf("invalid authoring scalar was accepted: %#v", value)
		}
	}
	if !validWorkflowAuthoringReadiness(WorkflowDependencyReadinessReady) ||
		validWorkflowAuthoringReadiness(WorkflowDependencyReadinessCode("unknown")) {
		t.Fatal("workflow authoring readiness classification mismatch")
	}
	if err := validateWorkflowAuthoringLimits([]WorkflowAuthoringLimitCode{
		WorkflowAuthoringAgentsTruncated,
	}); err != nil {
		t.Fatalf("valid workflow authoring limits failed: %v", err)
	}
	if err := validateWorkflowAuthoringLimits([]WorkflowAuthoringLimitCode{"unknown"}); err == nil {
		t.Fatal("unknown workflow authoring limit was accepted")
	}
	if err := validateWorkflowAuthoringLimits([]WorkflowAuthoringLimitCode{
		WorkflowAuthoringAgentsTruncated, WorkflowAuthoringAgentsTruncated,
	}); err == nil {
		t.Fatal("unsorted workflow authoring limits were accepted")
	}
	if !workflowAuthoringLimitPresent(
		[]WorkflowAuthoringLimitCode{WorkflowAuthoringAgentsTruncated},
		WorkflowAuthoringAgentsTruncated,
	) || workflowAuthoringLimitPresent(nil, WorkflowAuthoringAgentsTruncated) {
		t.Fatal("workflow authoring limit lookup mismatch")
	}

	var decoded WorkflowAuthoringScalar
	if err := json.Unmarshal([]byte(`"round-trip"`), &decoded); err != nil {
		t.Fatalf("JSON scalar round-trip failed: %v", err)
	}
}

func TestWorkflowMutationAndDevelopmentCloseoutContracts(t *testing.T) {
	workspace := t.TempDir()
	if err := WithWorkflowMutationLockAndDevelopmentSession(workspace, nil); err == nil {
		t.Fatal("nil development-session mutation was accepted")
	}
	called := false
	if err := WithWorkflowMutationLockAndDevelopmentSession(
		workspace,
		func(session *WorkflowDevelopmentSession) error {
			called = true
			if session != nil {
				t.Fatalf("unexpected active development session: %#v", session)
			}
			return nil
		},
	); err != nil || !called {
		t.Fatalf("development-session mutation = (called=%t, err=%v)", called, err)
	}

	session, startErr := StartWorkflowDevelopment(
		context.Background(),
		workspace,
		RuntimeCompatibility{},
		WorkflowDevelopmentStartRequest{Prompt: "review events", TargetRef: "workflows/review-events.yml"},
	)
	if startErr != nil {
		t.Fatal(startErr)
	}
	discarded, discardErr := DiscardWorkflowDevelopment(workspace)
	if discardErr != nil || discarded.ID != session.ID {
		t.Fatalf("discarded development = (%#v, %v)", discarded, discardErr)
	}
	active, activeErr := GetWorkflowDevelopmentSession(workspace)
	if activeErr != nil || active != nil {
		t.Fatalf("active development after discard = (%#v, %v)", active, activeErr)
	}
	if _, discardErr := DiscardWorkflowDevelopment(workspace); !errors.Is(discardErr, ErrNoActiveDevelopment) {
		t.Fatalf("second discard error = %v", discardErr)
	}

	loader := ReusableWorkflowLoaderFunc(func(_ context.Context, ref string) (*Workflow, error) {
		return &Workflow{Name: ref}, nil
	})
	loaded, loadErr := loader.LoadReusableWorkflow(context.Background(), "workflows/reusable.yml")
	if loadErr != nil || loaded.Name != "workflows/reusable.yml" {
		t.Fatalf("reusable workflow loader = (%#v, %v)", loaded, loadErr)
	}
	if err := ValidatePrivateGateActionWorkflow(nil); err == nil {
		t.Fatal("nil private gate action workflow was accepted")
	}
}
