package workflows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNativeWorkflowStateFailureAndDefaultContracts(t *testing.T) {
	exec := ExecutionContext{WorkspaceDir: t.TempDir(), WorkflowRef: "workflows/test.yml", RunID: "wr_test"}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := nativeWorkflowState(canceled, nil, exec); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled state error = %v", err)
	}
	for _, args := range []map[string]any{
		{},
		{"action": "set"},
		{"action": "set", "key": "item"},
		{"action": "delete"},
		{"action": "unsupported", "key": "item"},
	} {
		if _, err := nativeWorkflowState(context.Background(), args, exec); err == nil {
			t.Fatalf("invalid workflow.state args %#v were accepted", args)
		}
	}
	missing, err := nativeWorkflowState(context.Background(), map[string]any{"key": "missing"}, exec)
	if err != nil || missing["exists"] != false {
		t.Fatalf("missing state = (%#v, %v)", missing, err)
	}
	deleted, err := nativeWorkflowState(
		context.Background(), map[string]any{"action": "delete", "key": "missing"}, exec,
	)
	if err != nil || deleted["deleted"] != false {
		t.Fatalf("missing delete = (%#v, %v)", deleted, err)
	}
	listed, err := nativeWorkflowState(
		context.Background(), map[string]any{"action": "list", "include_values": true}, exec,
	)
	keys, _ := listed["keys"].([]string)
	if err != nil || len(keys) != 0 {
		t.Fatalf("empty state list = (%#v, %v)", listed, err)
	}
	if _, err := nativeWorkflowState(
		context.Background(),
		map[string]any{"action": "set", "key": "bad", "value": make(chan int)},
		exec,
	); err == nil {
		t.Fatal("non-JSON state value was accepted")
	}
}

func TestNativeWorkflowArtifactFailureAndDefaultContracts(t *testing.T) {
	exec := ExecutionContext{WorkspaceDir: t.TempDir(), WorkflowRef: "workflows/test.yml", RunID: "wr_test"}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := nativeWorkflowArtifact(canceled, nil, exec); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled artifact error = %v", err)
	}
	for _, args := range []map[string]any{
		{"action": "read"},
		{"action": "unsupported"},
		{"action": "write", "name": "missing.txt"},
		{"action": "write", "name": "bad.json", "value": make(chan int)},
		{"action": "write", "name": "../escape.txt", "content": "bad"},
	} {
		if _, err := nativeWorkflowArtifact(context.Background(), args, exec); err == nil {
			t.Fatalf("invalid workflow.artifact args %#v were accepted", args)
		}
	}
	listed, err := nativeWorkflowArtifact(context.Background(), nil, exec)
	if err != nil || listed["artifacts"] == nil {
		t.Fatalf("empty artifact list = (%#v, %v)", listed, err)
	}
	for _, format := range []string{"json", "markdown", "text"} {
		name := defaultArtifactName(map[string]any{"format": format})
		if name == "" || !strings.HasPrefix(name, "artifact-") {
			t.Fatalf("default %s artifact name = %q", format, name)
		}
	}
}

func TestNativeGitAndFilterFailuresStayBounded(t *testing.T) {
	exec := ExecutionContext{WorkspaceDir: t.TempDir()}
	if _, err := nativeGit(context.Background(), exec.WorkspaceDir, "not-a-command"); err == nil {
		t.Fatal("invalid git command succeeded")
	}
	if _, exceeded, err := nativeGitBoundedOutput(
		context.Background(), exec.WorkspaceDir, 4, "not-a-command",
	); err == nil || exceeded {
		t.Fatalf("invalid bounded git = (exceeded=%t, err=%v)", exceeded, err)
	}
	gitErr := nativeGitError{err: errors.New("failed"), args: []string{"status"}}
	if !strings.Contains(gitErr.Error(), "git status failed") {
		t.Fatalf("git error = %q", gitErr.Error())
	}
	if _, err := nativeGitInventoryOutputFiles(
		nativeGitWorkspaceRef{},
		[]nativeGitFile{{Path: "../escape", BlobHash: "hash"}},
		"all",
		true,
	); err == nil {
		t.Fatal("escaping inventory path was accepted")
	}
	if _, err := nativeGitFilter(context.Background(), nil, exec); err == nil {
		t.Fatal("filter without files was accepted")
	}
	if _, err := nativeGitFilter(context.Background(), map[string]any{
		"files": []any{map[string]any{"path": "../escape"}},
	}, exec); err == nil {
		t.Fatal("filter accepted escaping path")
	}
}

func TestNativePathClassificationAndGlobMatrix(t *testing.T) {
	classifications := map[string]string{
		"vendor/module.go":    "excluded",
		"pkg/service_test.go": "tests",
		"pkg/service.go":      "code",
		"config.yaml":         "code",
		"go.sum":              "excluded",
		"README":              "excluded",
	}
	for file, want := range classifications {
		if got := nativeCategorizePath(file); got != want {
			t.Fatalf("nativeCategorizePath(%q) = %q, want %q", file, got, want)
		}
	}
	for _, test := range []struct {
		pattern string
		path    string
		want    bool
	}{
		{pattern: "**/*.go", path: "pkg/file.go", want: true},
		{pattern: "pkg/", path: "pkg/nested/file.go", want: true},
		{pattern: "pkg", path: "nested/pkg/file.go", want: true},
		{pattern: "pkg/?.go", path: "pkg/a.go", want: true},
		{pattern: "pkg/[", path: "pkg/a.go", want: false},
	} {
		if got := nativeGlobMatches(test.pattern, test.path); got != test.want {
			t.Fatalf("nativeGlobMatches(%q, %q) = %t", test.pattern, test.path, got)
		}
	}
	if nativeTargetSelects("code", "tests") || !nativeTargetSelects("all", "tests") {
		t.Fatal("target classification mismatch")
	}
	if normalizeFileTarget("unknown") != "code" {
		t.Fatal("unknown target did not fall back to code")
	}
}

func TestNativeStoragePathValidationAndCorruption(t *testing.T) {
	exec := ExecutionContext{WorkspaceDir: t.TempDir()}
	if _, err := nativeStatePath(exec, "namespace", " "); err == nil {
		t.Fatal("blank state key was accepted")
	}
	for _, name := range []string{"", "/absolute", "../escape"} {
		if _, err := safeArtifactRel(name); err == nil {
			t.Fatalf("unsafe artifact path %q was accepted", name)
		}
	}
	if _, _, err := nativeArtifactPath(exec, "namespace", "run", "../escape"); err == nil {
		t.Fatal("escaping artifact path was accepted")
	}
	if _, err := nativeConfinedPath(exec, "..", "escape"); err == nil {
		t.Fatal("escaping confined path was accepted")
	}

	statePath, err := nativeStatePath(exec, "namespace", "corrupt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readNativeStateValue(exec, "namespace", "corrupt"); err == nil {
		t.Fatal("corrupt state value was accepted")
	}
	if _, _, err := listNativeStateValues(exec, "namespace", true); err == nil {
		t.Fatal("corrupt state list entry was accepted")
	}
}

func TestNativeArgumentCoercionMatrix(t *testing.T) {
	if nativeString(nil, "missing") != "" || nativeString(map[string]any{"nil": nil}, "nil") != "" {
		t.Fatal("nil native string was not empty")
	}
	if nativeStringDefault(nil, "missing", "fallback") != "fallback" {
		t.Fatal("native string fallback was not used")
	}
	if got := nativeMapValue(map[string]string{"key": "value"}); got["key"] != "value" {
		t.Fatalf("string map projection = %#v", got)
	}
	if got := nativeMapValue(`{"key":"value"}`); got["key"] != "value" {
		t.Fatalf("JSON map projection = %#v", got)
	}
	if nativeMapValue("not-json") != nil || nativeMapValue(true) != nil {
		t.Fatal("invalid native map value was accepted")
	}
	for _, value := range []any{nil, []any{"not-object"}, "not-json", true} {
		if _, err := nativeMapSlice(value); err == nil {
			t.Fatalf("invalid map slice %#v was accepted", value)
		}
	}
	if got, err := nativeMapSlice(`[{"key":"value"}]`); err != nil || got[0]["key"] != "value" {
		t.Fatalf("JSON map slice = (%#v, %v)", got, err)
	}

	stringCases := []struct {
		value any
		want  []string
	}{
		{value: nil, want: nil},
		{value: " one, two ", want: []string{"one", "two"}},
		{value: `["one","two"]`, want: []string{"one", "two"}},
		{value: []any{" one ", nil}, want: []string{"one"}},
		{value: true, want: nil},
	}
	for _, test := range stringCases {
		if got := nativeStringSlice(test.value); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("nativeStringSlice(%#v) = %#v, want %#v", test.value, got, test.want)
		}
	}
	if !nativeBool(map[string]any{"value": " yes "}, "value") ||
		nativeBool(map[string]any{"value": "no"}, "value") ||
		nativeBool(map[string]any{"value": 1}, "value") {
		t.Fatal("native bool coercion mismatch")
	}
	for _, test := range []struct {
		value any
		want  int
	}{
		{value: int64(2), want: 2},
		{value: float64(3), want: 3},
		{value: "4", want: 4},
		{value: "bad", want: 9},
	} {
		if got := nativeInt(map[string]any{"value": test.value}, "value", 9); got != test.want {
			t.Fatalf("nativeInt(%#v) = %d, want %d", test.value, got, test.want)
		}
	}
	if _, err := nativeStableHash(make(chan int)); err == nil {
		t.Fatal("non-JSON stable hash input was accepted")
	}
}
