package gateway

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	providercommon "github.com/sipeed/picoclaw/pkg/providers/common"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/prworkspace/localci"
)

const (
	codingAgentBenchmarkFixtureID       = "transfer-idempotency-v1"
	codingAgentBenchmarkAgentID         = "benchmark"
	codingAgentBenchmarkLiveEnv         = "PICOCLAW_CODING_AGENT_BENCHMARK_LIVE"
	codingAgentBenchmarkConfigEnv       = "PICOCLAW_CODING_AGENT_BENCHMARK_CONFIG"
	codingAgentBenchmarkOutputEnv       = "PICOCLAW_CODING_AGENT_BENCHMARK_OUTPUT"
	codingAgentBenchmarkSandboxEnv      = "PICOCLAW_CODING_AGENT_BENCHMARK_SANDBOX"
	codingAgentBenchmarkGraderMaxOutput = 64 << 10
)

// TestCodingAgentBenchmarkScriptedGatewayPath is deliberately an ordinary test.
// It exercises the production Gateway repair adapter, LocalRepairRunner, and
// workflowAgentRunner-backed scope/completion AI without network access. LocalCI
// results are scripted from the tracked production plan so the repair-feedback
// cycle remains deterministic on hosts that cannot create the production
// sandbox.
func TestCodingAgentBenchmarkScriptedGatewayPath(t *testing.T) {
	requireCodingAgentBenchmarkGit(t)
	fixture := newCodingAgentBenchmarkFixture(t)
	provider := newCodingAgentBenchmarkScriptedProvider(t, fixture)
	validation := newCodingAgentBenchmarkScriptedValidation(t, fixture.visibleRoot)
	artifactRoot := t.TempDir()
	if err := os.Chmod(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	result := runCodingAgentBenchmark(t, codingAgentBenchmarkRunOptions{
		fixture: fixture, provider: provider, validation: validation,
		validationBackend: "scripted-local-ci-plan", runGrader: true,
		artifactRoot: artifactRoot,
	})
	assertCodingAgentBenchmarkResult(t, result, provider, true)
	for _, name := range []string{"candidate.patch", "grader-v2.json", "manifest-v2.json"} {
		info, err := os.Stat(filepath.Join(artifactRoot, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("scripted artifact %q mode = %v, error = %v", name, info, err)
		}
	}
}

// TestCodingAgentBenchmarkProductionSandboxOptIn runs exactly the same
// scripted provider through the package-owned production LocalCI sandbox. It is
// opt-in because bubblewrap plus the required resource authority are not
// available on every developer or CI host.
func TestCodingAgentBenchmarkProductionSandboxOptIn(t *testing.T) {
	if os.Getenv(codingAgentBenchmarkSandboxEnv) != "1" {
		t.Skip("set " + codingAgentBenchmarkSandboxEnv + "=1 to run the production sandbox benchmark")
	}
	requireCodingAgentBenchmarkGit(t)
	fixture := newCodingAgentBenchmarkFixture(t)
	provider := newCodingAgentBenchmarkScriptedProvider(t, fixture)

	result := runCodingAgentBenchmark(t, codingAgentBenchmarkRunOptions{
		fixture: fixture, provider: provider,
		productionSandbox: true, validationBackend: "production-local-ci", runGrader: true,
	})
	assertCodingAgentBenchmarkResult(t, result, provider, true)
}

// TestCodingAgentBenchmarkLiveOptIn keeps authenticated model calls out of
// ordinary CI. The output directory must be a new absolute path outside the
// repository; raw patch and manifest files are created with private modes.
func TestCodingAgentBenchmarkLiveOptIn(t *testing.T) {
	if os.Getenv(codingAgentBenchmarkLiveEnv) != "1" {
		t.Skip("set " + codingAgentBenchmarkLiveEnv + "=1 to run authenticated benchmark calls")
	}
	requireCodingAgentBenchmarkGit(t)
	configPath := strings.TrimSpace(os.Getenv(codingAgentBenchmarkConfigEnv))
	outputPath := strings.TrimSpace(os.Getenv(codingAgentBenchmarkOutputEnv))
	if configPath == "" || outputPath == "" {
		t.Fatalf("live benchmark requires %s and %s", codingAgentBenchmarkConfigEnv, codingAgentBenchmarkOutputEnv)
	}
	outputRoot := prepareCodingAgentBenchmarkPrivateOutput(t, outputPath)
	fixture := newCodingAgentBenchmarkFixture(t)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load live benchmark config: %v", err)
	}
	if err = validateCodingAgentBenchmarkLiveProfile(cfg); err != nil {
		t.Fatalf("live benchmark profile is not direct low-effort: %v", err)
	}
	controlRoot := filepath.Join(t.TempDir(), "live-control")
	if err = os.Mkdir(controlRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	bindCodingAgentBenchmarkWorkspace(cfg, controlRoot)
	provider, _, err := providers.CreateProvider(cfg)
	if err != nil {
		t.Fatalf("create live benchmark provider: %v", err)
	}

	result := runCodingAgentBenchmark(t, codingAgentBenchmarkRunOptions{
		fixture: fixture, provider: provider, config: cfg,
		productionSandbox: true, validationBackend: "production-local-ci",
		artifactRoot: outputRoot,
	})
	if result.aggregate.Workspace.Phase != prworkspace.PhasePublication ||
		result.aggregate.Workspace.ExecutionState != prworkspace.ExecutionWaitingGate {
		t.Fatalf("live benchmark did not stop at the publication fence: %#v", result.aggregate.Workspace)
	}
	if len(result.aggregate.Publications) != 0 {
		t.Fatalf("live benchmark crossed publication fence: %#v", result.aggregate.Publications)
	}
	if result.manifest.Grader == nil || !result.manifest.Grader.MandatoryPass ||
		result.manifest.Grader.Score != 100 || len(result.manifest.CensorReasons) != 0 {
		t.Fatalf("live benchmark did not retain a complete sandboxed grader: %#v", result.manifest)
	}
}

func TestCodingAgentBenchmarkLiveProfileRequiresDirectLowEffort(t *testing.T) {
	valid := codingAgentBenchmarkConfig(t.TempDir())
	if err := validateCodingAgentBenchmarkLiveProfile(valid); err != nil {
		t.Fatalf("valid direct low-effort profile: %v", err)
	}

	for name, mutate := range map[string]func(*config.Config){
		"non-low effort": func(cfg *config.Config) {
			cfg.ModelList[0].ReasoningEffort = "medium"
		},
		"fallback": func(cfg *config.Config) {
			cfg.Agents.List[0].Model.Fallbacks = []string{"benchmark-model"}
		},
		"light router": func(cfg *config.Config) {
			cfg.Agents.Defaults.Routing = &config.RoutingConfig{
				Enabled: true, LightModel: "benchmark-model", Threshold: 0.5,
			}
		},
		"duplicate account": func(cfg *config.Config) {
			duplicate := *cfg.ModelList[0]
			cfg.ModelList = append(cfg.ModelList, &duplicate)
		},
		"non-openai provider": func(cfg *config.Config) {
			cfg.ModelList[0].Provider = "codex-cli"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := codingAgentBenchmarkConfig(t.TempDir())
			mutate(candidate)
			if err := validateCodingAgentBenchmarkLiveProfile(candidate); err == nil {
				t.Fatal("unsafe live profile was accepted")
			}
		})
	}
}

func TestCodingAgentBenchmarkSandboxedGraderContractIsBounded(t *testing.T) {
	step := codingAgentBenchmarkSandboxedGraderStep(strings.Repeat("a", 40))
	if step.Shell != "bash" || step.WorkingDirectory != "" || len(step.Argv) != 0 ||
		len(step.Environment) != 1 || step.Environment[0].Name != "EXPECTED_COMMIT" {
		t.Fatalf("sandboxed grader step = %#v", step)
	}
	for _, required := range []string{
		"/dependencies/candidate", "/dependencies/grader/grade.sh",
		"/tmp/picoclaw-grader-candidate", "/tmp/picoclaw-grader-output",
	} {
		if !strings.Contains(step.Script, required) {
			t.Fatalf("sandboxed grader step omitted %q", required)
		}
	}
	if strings.Contains(step.Script, codingAgentBenchmarkRepositoryRoot(t)) {
		t.Fatal("sandboxed grader step contains a host repository path")
	}

	digest := "sha256:" + strings.Repeat("b", 64)
	mutants := make([]any, 5)
	for index := range mutants {
		mutants[index] = map[string]any{"id": fmt.Sprintf("mutant-%d", index), "killed": true}
	}
	valid, err := json.Marshal(map[string]any{
		"version": 2, "fixture": codingAgentBenchmarkFixtureID,
		"score": 100, "mandatory_pass": true,
		"checks": map[string]any{
			"format": true, "vet": true, "test": true, "race": true,
			"scope": true, "git_head": true, "tests_changed": true,
		},
		"mutation": map[string]any{
			"killed": 5, "total": 5, "points": 10, "mutants": mutants,
		},
		"patch_sha256": digest, "grader_sha256": digest,
		"hidden_test_sha256": digest, "mutants_sha256": digest,
		"changed_files": []string{"ledger/ledger.go", "ledger/ledger_candidate_test.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeCodingAgentBenchmarkGraderArtifact(valid); err != nil {
		t.Fatalf("valid grader artifact rejected: %v", err)
	}
	if _, err = decodeCodingAgentBenchmarkGraderArtifact(append(valid, []byte(`{}`)...)); err == nil {
		t.Fatal("grader artifact with trailing JSON was accepted")
	}
	if _, err = decodeCodingAgentBenchmarkGraderArtifact(
		make([]byte, codingAgentBenchmarkGraderMaxOutput+1),
	); err == nil {
		t.Fatal("oversized grader artifact was accepted")
	}
}

type codingAgentBenchmarkFixture struct {
	visibleRoot     string
	graderRoot      string
	originRoot      string
	bareRepository  string
	head            string
	tree            string
	taskDigest      string
	originalLedger  string
	referenceLedger string
	referenceTests  string
	remoteRefs      string
}

func newCodingAgentBenchmarkFixture(t *testing.T) codingAgentBenchmarkFixture {
	t.Helper()
	repositoryRoot := codingAgentBenchmarkRepositoryRoot(t)
	visibleSource := filepath.Join(
		repositoryRoot, "integration", "fixtures", "coding-agent-benchmark", codingAgentBenchmarkFixtureID,
	)
	graderRoot := filepath.Join(
		repositoryRoot, "integration", "codingagentbenchmark", codingAgentBenchmarkFixtureID,
	)
	visibleRoot := filepath.Join(t.TempDir(), "model-visible")
	copyCodingAgentBenchmarkTree(t, visibleSource, visibleRoot)
	runCodingAgentBenchmarkGit(t, visibleRoot, "init")
	runCodingAgentBenchmarkGit(t, visibleRoot, "checkout", "-b", "main")
	runCodingAgentBenchmarkGit(t, visibleRoot, "add", ".")
	runCodingAgentBenchmarkGit(
		t, visibleRoot,
		"-c", "user.name=PicoClaw Benchmark", "-c", "user.email=benchmark@example.invalid",
		"commit", "-m", "benchmark fixture",
	)
	head := runCodingAgentBenchmarkGit(t, visibleRoot, "rev-parse", "HEAD")
	tree := runCodingAgentBenchmarkGit(t, visibleRoot, "rev-parse", "HEAD^{tree}")

	originRoot := filepath.Join(t.TempDir(), "origin")
	bareRepository := filepath.Join(originRoot, "benchmark", codingAgentBenchmarkFixtureID+".git")
	if err := os.MkdirAll(filepath.Dir(bareRepository), 0o700); err != nil {
		t.Fatal(err)
	}
	runCodingAgentBenchmarkGit(t, visibleRoot, "clone", "--quiet", "--bare", visibleRoot, bareRepository)

	read := func(path string) string {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	fixture := codingAgentBenchmarkFixture{
		visibleRoot:     visibleRoot,
		graderRoot:      graderRoot,
		originRoot:      originRoot,
		bareRepository:  bareRepository,
		head:            head,
		tree:            tree,
		taskDigest:      codingAgentBenchmarkDigest("task", read(filepath.Join(visibleSource, "task.md"))),
		originalLedger:  read(filepath.Join(visibleSource, "ledger", "ledger.go")),
		referenceLedger: read(filepath.Join(graderRoot, "testdata", "reference", "ledger.go")),
		referenceTests:  read(filepath.Join(graderRoot, "testdata", "reference", "ledger_candidate_test.go")),
	}
	fixture.remoteRefs = runCodingAgentBenchmarkGit(
		t, bareRepository, "for-each-ref", "--format=%(refname):%(objectname)", "refs/heads",
	)
	return fixture
}

type codingAgentBenchmarkScriptedProvider struct {
	mu               sync.Mutex
	originalLedger   string
	referenceLedger  string
	referenceTests   string
	repairLoops      int
	providerCalls    int
	toolCalls        int
	scopeCalls       int
	completionCalls  int
	feedbackObserved bool
	apiBase          string
}

func newCodingAgentBenchmarkScriptedProvider(
	t *testing.T,
	fixture codingAgentBenchmarkFixture,
) *codingAgentBenchmarkScriptedProvider {
	t.Helper()
	provider := &codingAgentBenchmarkScriptedProvider{
		originalLedger:  fixture.originalLedger,
		referenceLedger: fixture.referenceLedger,
		referenceTests:  fixture.referenceTests,
	}
	server := httptest.NewServer(http.HandlerFunc(provider.serveHTTP))
	provider.apiBase = server.URL
	t.Cleanup(server.Close)
	return provider
}

func (provider *codingAgentBenchmarkScriptedProvider) serveHTTP(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Header.Get("Authorization") != "Bearer benchmark-key" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var request struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Model string `json:"model"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	messages := make([]providers.Message, 0, len(request.Messages))
	for _, message := range request.Messages {
		messages = append(messages, providers.Message{Role: message.Role, Content: message.Content})
	}
	response, err := provider.Chat(r.Context(), messages, nil, request.Model, nil)
	if err != nil {
		http.Error(w, "scripted provider failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{
			"message":       map[string]any{"content": response.Content},
			"finish_reason": response.FinishReason,
		}},
		"usage": response.Usage,
	})
}

func (provider *codingAgentBenchmarkScriptedProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	definitions []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.providerCalls++
	usage := &providers.UsageInfo{
		PromptTokens: 40, CachedTokens: 10,
		CompletionTokens: 10, ReasoningTokens: 2, TotalTokens: 50,
	}
	if len(definitions) > 0 {
		if codingAgentBenchmarkHasToolResult(messages) {
			return &providers.LLMResponse{
				Content: "Applied the focused transfer changes.", FinishReason: "stop", Usage: usage,
			}, nil
		}
		provider.repairLoops++
		provider.toolCalls++
		userText := codingAgentBenchmarkUserMessage(messages)
		if provider.repairLoops == 2 &&
			strings.Contains(userText, `"validation"`) &&
			strings.Contains(userText, `"failed"`) {
			provider.feedbackObserved = true
		}
		var name string
		var arguments map[string]any
		switch provider.repairLoops {
		case 1:
			name = "apply_patch"
			arguments = map[string]any{
				"patch": codingAgentBenchmarkAddFilePatch(
					"ledger/ledger_candidate_test.go", provider.referenceTests,
				),
			}
		case 2:
			name = "edit_file"
			arguments = map[string]any{
				"path": "ledger/ledger.go", "old_text": provider.originalLedger,
				"new_text": provider.referenceLedger,
			}
		default:
			return nil, fmt.Errorf("scripted provider received unexpected repair loop %d", provider.repairLoops)
		}
		rawArguments, err := json.Marshal(arguments)
		if err != nil {
			return nil, err
		}
		return &providers.LLMResponse{
			FinishReason: "tool_calls", Usage: usage,
			ToolCalls: []providers.ToolCall{{
				ID: fmt.Sprintf("benchmark-edit-%d", provider.repairLoops), Type: "function",
				Name: name, Arguments: arguments,
				Function: &providers.FunctionCall{Name: name, Arguments: string(rawArguments)},
			}},
		}, nil
	}

	system := codingAgentBenchmarkSystemMessage(messages)
	switch {
	case strings.HasPrefix(system, "Audit the exact candidate diff"):
		provider.scopeCalls++
		content, err := codingAgentBenchmarkScopeResponse(codingAgentBenchmarkUserMessage(messages))
		if err != nil {
			return nil, err
		}
		return &providers.LLMResponse{Content: content, FinishReason: "stop", Usage: usage}, nil
	case strings.HasPrefix(system, "Determine whether every confirmed acceptance criterion"):
		provider.completionCalls++
		return &providers.LLMResponse{
			Content:      `{"summary":"Transfer behavior is complete and validated.","complete":true,"missing_in_scope":[],"out_of_scope":[],"coverage":{"reviewed_areas":["ledger transfer"],"unreviewed_areas":[],"tests_considered":["format","vet","test","race"],"residual_risks":[]}}`,
			FinishReason: "stop",
			Usage:        usage,
		}, nil
	default:
		return nil, fmt.Errorf("scripted provider received unexpected isolated request")
	}
}

func codingAgentBenchmarkHasToolResult(messages []providers.Message) bool {
	for _, message := range messages {
		if message.Role == "tool" {
			return true
		}
	}
	return false
}

func codingAgentBenchmarkSystemMessage(messages []providers.Message) string {
	for _, message := range messages {
		if message.Role == "system" {
			return message.Content
		}
	}
	return ""
}

func codingAgentBenchmarkUserMessage(messages []providers.Message) string {
	for _, message := range messages {
		if message.Role == "user" {
			return message.Content
		}
	}
	return ""
}

func codingAgentBenchmarkAddFilePatch(path, content string) string {
	var builder strings.Builder
	builder.WriteString("*** Begin Patch\n*** Add File: ")
	builder.WriteString(path)
	builder.WriteByte('\n')
	for _, line := range strings.SplitAfter(content, "\n") {
		if line == "" {
			continue
		}
		builder.WriteByte('+')
		builder.WriteString(strings.TrimSuffix(line, "\n"))
		builder.WriteByte('\n')
	}
	builder.WriteString("*** End Patch")
	return builder.String()
}

type codingAgentBenchmarkPromptBundle struct {
	CandidateDiff    string                       `json:"candidate_diff"`
	CandidateMetrics prworkspace.CandidateMetrics `json:"candidate_metrics"`
}

type codingAgentBenchmarkDiffHunk struct {
	path   string
	header string
	lines  int
}

func codingAgentBenchmarkScopeResponse(userMessage string) (string, error) {
	jsonStart := strings.IndexByte(userMessage, '{')
	if jsonStart < 0 {
		return "", errors.New("scope prompt contains no context object")
	}
	var bundle codingAgentBenchmarkPromptBundle
	decoder := json.NewDecoder(strings.NewReader(userMessage[jsonStart:]))
	if err := decoder.Decode(&bundle); err != nil {
		return "", fmt.Errorf("decode scope prompt: %w", err)
	}
	hunks := codingAgentBenchmarkDiffHunks(bundle.CandidateDiff)
	if len(hunks) == 0 {
		return "", errors.New("scope prompt contains no candidate hunks")
	}
	size := prworkspace.ClassifyChangeSize(
		bundle.CandidateMetrics.Files,
		bundle.CandidateMetrics.SemanticLines,
		bundle.CandidateMetrics.Modules,
		prworkspace.DefaultSizePolicy(),
	)
	changes := make([]map[string]any, 0, len(hunks))
	for _, hunk := range hunks {
		changes = append(changes, map[string]any{
			"path": hunk.path, "hunk": hunk.header,
			"module": codingAgentBenchmarkModule(hunk.path), "semantic_lines": hunk.lines,
			"presence":       string(prworkspace.WorkCandidatePresent),
			"scope_distance": string(prworkspace.ScopeExact), "change_size": string(size),
			"type_compatible": true, "confidence": 1.0,
			"charter_clauses": []string{"goal", "acceptance criteria"},
			"explanation":     "The hunk directly implements or validates the transfer contract.",
		})
	}
	response := map[string]any{
		"changes":              changes,
		"files":                bundle.CandidateMetrics.Files,
		"semantic_lines":       bundle.CandidateMetrics.SemanticLines,
		"modules":              bundle.CandidateMetrics.Modules,
		"worst_scope_distance": string(prworkspace.ScopeExact),
		"worst_change_size":    string(size), "type_compatible": true, "confidence": 1.0,
		"charter_clauses": []string{"goal", "acceptance criteria"},
		"explanation":     "Every exact candidate hunk is within the confirmed feature charter.",
	}
	encoded, err := json.Marshal(response)
	return string(encoded), err
}

func codingAgentBenchmarkDiffHunks(diff string) []codingAgentBenchmarkDiffHunk {
	lines := strings.Split(diff, "\n")
	var result []codingAgentBenchmarkDiffHunk
	path := ""
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			path = strings.TrimPrefix(line, "+++ b/")
		case strings.HasPrefix(line, "@@ "):
			closing := strings.Index(line[3:], "@@")
			if closing < 0 || path == "" {
				continue
			}
			headerEnd := 3 + closing + 2
			result = append(result, codingAgentBenchmarkDiffHunk{
				path: path, header: line[:headerEnd],
			})
		case len(result) > 0 &&
			(strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") ||
				strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---")):
			result[len(result)-1].lines++
		}
	}
	return result
}

func codingAgentBenchmarkModule(path string) string {
	path = filepath.ToSlash(path)
	if before, _, ok := strings.Cut(path, "/"); ok {
		return before
	}
	return "."
}

type codingAgentBenchmarkScriptedValidation struct {
	mu    sync.Mutex
	steps []localci.Step
	calls int
}

func newCodingAgentBenchmarkScriptedValidation(
	t *testing.T,
	fixtureRoot string,
) *codingAgentBenchmarkScriptedValidation {
	t.Helper()
	plan, err := localci.Discover(t.Context(), fixtureRoot)
	if err != nil {
		t.Fatalf("discover benchmark LocalCI plan: %v", err)
	}
	if !plan.Complete || len(plan.Steps) != 4 {
		t.Fatalf("benchmark LocalCI plan = %#v, want four complete steps", plan)
	}
	return &codingAgentBenchmarkScriptedValidation{steps: append([]localci.Step(nil), plan.Steps...)}
}

func (validation *codingAgentBenchmarkScriptedValidation) Validate(
	_ context.Context,
	request prworkspace.ValidationRequest,
) (prworkspace.ValidationRun, error) {
	validation.mu.Lock()
	defer validation.mu.Unlock()
	validation.calls++
	now := time.Date(2026, 8, 30, 12, 0, validation.calls, 0, time.UTC)
	state := prworkspace.ExecutionSucceeded
	checks := make([]prworkspace.ValidationCheck, 0, len(validation.steps))
	for _, step := range validation.steps {
		status := "passed"
		summary := "scripted production-plan check passed"
		exitCode := 0
		if validation.calls == 1 && step.ID == "test" {
			state, status, exitCode = prworkspace.ExecutionFailed, "failed", 1
			summary = "candidate tests expose the still-unimplemented transfer behavior"
		}
		checks = append(checks, prworkspace.ValidationCheck{
			ID: step.ID, Name: step.Name, Status: status, Summary: summary,
			ExitCode: &exitCode, DurationMS: 1,
		})
	}
	return prworkspace.ValidationRun{
		ID: request.ID, State: state, CandidateSHA: request.CandidateSHA,
		Checks: checks, StartedAt: now, FinishedAt: &now,
	}, nil
}

type codingAgentBenchmarkGateEvaluator struct{}

func (codingAgentBenchmarkGateEvaluator) Start(
	_ context.Context,
	request prworkspace.GateRequest,
) (prworkspace.GateRun, error) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	action := map[string]string{
		"pr.implementation.eligibility": "authorize",
		"pr.implementation.start":       "continue",
		"pr.implementation.scope":       "approve",
		"pr.implementation.complete":    "accept",
	}[request.DecisionPoint]
	if action == "" {
		return prworkspace.GateRun{}, fmt.Errorf("unexpected benchmark gate %q", request.DecisionPoint)
	}
	return prworkspace.GateRun{
		ID: codingAgentBenchmarkOpaqueID(
			"pgr_",
			request.WorkspaceID,
			request.DecisionPoint,
			request.SubjectDigest,
		),
		DecisionPoint:   request.DecisionPoint,
		State:           prworkspace.ExecutionSucceeded,
		PolicyRevision:  "sha256:" + strings.Repeat("a", 64),
		SubjectRevision: request.SubjectDigest,
		CreatedAt:       now,
		FinishedAt:      &now,
		Turns: []prworkspace.GateTurn{{
			StageID: "benchmark", Kind: "deterministic", ActorKind: "deterministic",
			Status: "answered", FieldValues: map[string]any{"action": action},
		}},
	}, nil
}

func (codingAgentBenchmarkGateEvaluator) Respond(
	_ context.Context,
	gate prworkspace.GateRun,
	_ map[string]any,
) (prworkspace.GateRun, error) {
	return gate, errors.New("benchmark gates never wait for a response")
}

type codingAgentBenchmarkRunOptions struct {
	fixture           codingAgentBenchmarkFixture
	provider          providers.LLMProvider
	config            *config.Config
	validation        prworkspace.ValidationExecutor
	productionSandbox bool
	validationBackend string
	runGrader         bool
	artifactRoot      string
	censorReasons     []string
}

type codingAgentBenchmarkResult struct {
	aggregate prworkspace.Aggregate
	manifest  codingAgentBenchmarkManifest
	patch     string
}

type codingAgentBenchmarkManifest struct {
	Version              int                             `json:"version"`
	Fixture              string                          `json:"fixture"`
	Mode                 string                          `json:"mode"`
	ValidationBackend    string                          `json:"validation_backend"`
	ProductCommit        string                          `json:"product_commit"`
	FixtureTree          string                          `json:"fixture_tree"`
	TaskSHA256           string                          `json:"task_sha256"`
	HarnessSourceSHA256  string                          `json:"harness_source_sha256"`
	BaseCommit           string                          `json:"base_commit"`
	CandidateCommit      string                          `json:"candidate_commit"`
	CandidateTree        string                          `json:"candidate_tree"`
	PatchSHA256          string                          `json:"patch_sha256"`
	EvidenceSHA256       string                          `json:"evidence_sha256"`
	IdentitySalt         string                          `json:"identity_salt"`
	ModelDigest          string                          `json:"model_digest"`
	AccountDigest        string                          `json:"account_digest"`
	RepairPromptDigests  []string                        `json:"repair_prompt_digests"`
	RepairProfileDigests []string                        `json:"repair_profile_digests"`
	Usage                prworkspace.ImplementationUsage `json:"usage"`
	RepairAttempts       int                             `json:"repair_attempts"`
	ValidationRuns       int                             `json:"validation_runs"`
	ProviderCalls        int64                           `json:"provider_calls"`
	DurationMillis       int64                           `json:"duration_millis"`
	RemoteUnchanged      bool                            `json:"remote_unchanged"`
	PublicationStopped   bool                            `json:"publication_stopped"`
	Grader               *codingAgentBenchmarkGrader     `json:"grader,omitempty"`
	CensorReasons        []string                        `json:"censor_reasons"`
}

type codingAgentBenchmarkGrader struct {
	Version        int    `json:"version"`
	Score          int    `json:"score"`
	MandatoryPass  bool   `json:"mandatory_pass"`
	ArtifactSHA256 string `json:"artifact_sha256"`
}

type codingAgentBenchmarkGraderArtifact struct {
	Version       int      `json:"version"`
	Fixture       string   `json:"fixture"`
	Score         int      `json:"score"`
	MandatoryPass bool     `json:"mandatory_pass"`
	ChangedFiles  []string `json:"changed_files"`
	Checks        struct {
		Format       bool `json:"format"`
		Vet          bool `json:"vet"`
		Test         bool `json:"test"`
		Race         bool `json:"race"`
		Scope        bool `json:"scope"`
		GitHead      bool `json:"git_head"`
		TestsChanged bool `json:"tests_changed"`
	} `json:"checks"`
	Mutation struct {
		Killed  int `json:"killed"`
		Total   int `json:"total"`
		Points  int `json:"points"`
		Mutants []struct {
			ID     string `json:"id"`
			Killed bool   `json:"killed"`
		} `json:"mutants"`
	} `json:"mutation"`
	PatchSHA256      string `json:"patch_sha256"`
	GraderSHA256     string `json:"grader_sha256"`
	HiddenTestSHA256 string `json:"hidden_test_sha256"`
	MutantsSHA256    string `json:"mutants_sha256"`
}

func runCodingAgentBenchmark(
	t *testing.T,
	options codingAgentBenchmarkRunOptions,
) codingAgentBenchmarkResult {
	t.Helper()
	if options.provider == nil {
		t.Fatal("benchmark provider is nil")
	}
	controlRoot := filepath.Join(t.TempDir(), "gateway-control")
	if err := os.Mkdir(controlRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := options.config
	if cfg == nil {
		cfg = codingAgentBenchmarkConfig(controlRoot)
	} else {
		bindCodingAgentBenchmarkWorkspace(cfg, controlRoot)
	}
	if scripted, ok := options.provider.(*codingAgentBenchmarkScriptedProvider); ok {
		for _, model := range cfg.ModelList {
			if model != nil && model.ModelName == "benchmark-account" {
				model.APIBase = scripted.apiBase
			}
		}
	}
	messageBus := bus.NewMessageBus()
	loop := agent.NewAgentLoop(cfg, messageBus, options.provider)
	t.Cleanup(func() {
		loop.Stop()
		loop.Close()
		messageBus.Close()
	})
	defaultAgent := loop.GetRegistry().GetDefaultAgent()
	if defaultAgent == nil || defaultAgent.ID == "" {
		t.Fatal("benchmark default agent is unavailable")
	}

	var ciRuntime *prWorkspaceLocalCIRuntime
	ciRunner := &localci.Runner{}
	if options.productionSandbox {
		var err error
		ciRuntime, err = newPRWorkspaceLocalCIRuntime(cfg)
		if err != nil {
			if errorsIsSandboxUnavailable(err) {
				t.Skipf("production LocalCI sandbox unavailable: %v", err)
			}
			t.Fatalf("initialize production LocalCI: %v", err)
		}
		t.Cleanup(func() {
			if err := ciRuntime.Close(); err != nil {
				t.Errorf("close production LocalCI: %v", err)
			}
		})
		ciRunner = ciRuntime.runner
	}
	runtimeAdapter, err := newPRWorkspaceImplementationRuntime(
		loop,
		ciRunner,
		defaultAgent.ID,
		func(ctx context.Context) (context.Context, func(), error) {
			return loop.AcquireRuntimeGeneration(ctx, cfg)
		},
	)
	if err != nil {
		t.Fatalf("create implementation runtime: %v", err)
	}
	validation := options.validation
	if validation == nil {
		validation = runtimeAdapter
	}
	service, aggregate, aiRecorder := seedCodingAgentBenchmarkService(t, runtimeAdapter, loop, options.fixture)

	outsideMarker := filepath.Join(t.TempDir(), "outside-marker")
	if err = os.WriteFile(outsideMarker, []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	aggregate, err = service.RunImplementation(t.Context(), prworkspace.ImplementationConfig{
		Repair: runtimeAdapter, Validation: validation, MaxCycles: 3,
	}, prworkspace.RunImplementationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID:   "benchmark-implementation-run-0001",
		FindingIDs:  []string{aggregate.Findings[0].ID},
		NudgePolicy: prworkspace.ConfiguredNudgePolicy(0, 0), MaxCycles: 3,
	})
	if err != nil {
		t.Fatalf("RunImplementation() error = %v; workspace = %#v", err, aggregate.Workspace)
	}
	duration := time.Since(started)
	if len(aggregate.RepairAttempts) == 0 {
		t.Fatal("benchmark produced no repair attempt")
	}
	finalRepair := aggregate.RepairAttempts[len(aggregate.RepairAttempts)-1]
	evidence, err := runtimeAdapter.LoadCandidateEvidence(t.Context(), finalRepair)
	if err != nil {
		providerState := "live"
		if scripted, ok := options.provider.(*codingAgentBenchmarkScriptedProvider); ok {
			scripted.mu.Lock()
			providerState = fmt.Sprintf(
				"calls=%d repairs=%d scopes=%d completions=%d",
				scripted.providerCalls, scripted.repairLoops, scripted.scopeCalls, scripted.completionCalls,
			)
			scripted.mu.Unlock()
		}
		t.Fatalf(
			"load finalized candidate evidence: %v; provider=%s ai_error=%v workspace=%#v stages=%#v repairs=%#v validations=%#v",
			err,
			providerState,
			aiRecorder.lastError(),
			aggregate.Workspace,
			aggregate.StageRuns,
			aggregate.RepairAttempts,
			aggregate.ValidationRuns,
		)
	}
	stage := aggregate.StageRuns[len(aggregate.StageRuns)-1]
	if stage.Usage == nil {
		t.Fatal("benchmark implementation has no v2 usage")
	}
	if !stage.Usage.Complete || stage.Usage.Total.ProviderCalls < 1 ||
		stage.Usage.Total.ProviderCalls != stage.Usage.Total.UsageReportedCalls {
		t.Fatalf("benchmark implementation usage is incomplete: %#v", stage.Usage)
	}
	remoteRefs := runCodingAgentBenchmarkGit(
		t, options.fixture.bareRepository,
		"for-each-ref", "--format=%(refname):%(objectname)", "refs/heads",
	)
	marker, err := os.ReadFile(outsideMarker)
	if err != nil || string(marker) != "unchanged\n" {
		t.Fatalf("benchmark changed outside marker: %q, %v", marker, err)
	}

	mode := "live"
	if _, ok := options.provider.(*codingAgentBenchmarkScriptedProvider); ok {
		mode = "scripted"
	}
	if options.runGrader && mode != "scripted" {
		t.Fatal("host-side benchmark grader is restricted to the trusted scripted provider")
	}
	identitySalt := codingAgentBenchmarkIdentitySalt(t, mode)
	promptDigests := make([]string, 0, len(aggregate.RepairAttempts))
	profileDigests := make([]string, 0, len(aggregate.RepairAttempts))
	for _, attempt := range aggregate.RepairAttempts {
		if !codingAgentBenchmarkOpaqueDigest(attempt.PromptDigest) ||
			!codingAgentBenchmarkOpaqueDigest(attempt.ProfileDigest) || !attempt.UsageComplete {
			t.Fatalf("repair attempt omitted opaque prompt/profile evidence: %#v", attempt)
		}
		promptDigests = append(promptDigests, attempt.PromptDigest)
		profileDigests = append(profileDigests, attempt.ProfileDigest)
	}
	manifest := codingAgentBenchmarkManifest{
		Version: 2, Fixture: codingAgentBenchmarkFixtureID,
		Mode: mode, ValidationBackend: options.validationBackend,
		ProductCommit: runCodingAgentBenchmarkGit(
			t, codingAgentBenchmarkRepositoryRoot(t), "rev-parse", "HEAD",
		),
		FixtureTree: options.fixture.tree, TaskSHA256: options.fixture.taskDigest,
		HarnessSourceSHA256: codingAgentBenchmarkHarnessSourceDigest(t),
		BaseCommit:          options.fixture.head, CandidateCommit: finalRepair.CandidateSHA,
		CandidateTree:       finalRepair.PublicationFence.Tree,
		PatchSHA256:         codingAgentBenchmarkDigest("patch", evidence.CandidateDiff),
		EvidenceSHA256:      codingAgentBenchmarkDigest("evidence", evidence.EvidenceDigest),
		IdentitySalt:        identitySalt,
		ModelDigest:         codingAgentBenchmarkIdentityDigest(identitySalt, "model", defaultAgent.Model),
		AccountDigest:       codingAgentBenchmarkIdentityDigest(identitySalt, "account", defaultAgent.AccountRef),
		RepairPromptDigests: promptDigests, RepairProfileDigests: profileDigests,
		Usage:          *stage.Usage,
		RepairAttempts: len(aggregate.RepairAttempts), ValidationRuns: len(aggregate.ValidationRuns),
		ProviderCalls:   stage.Usage.Total.ProviderCalls,
		DurationMillis:  max(int64(1), duration.Milliseconds()),
		RemoteUnchanged: remoteRefs == options.fixture.remoteRefs,
		PublicationStopped: aggregate.Workspace.Phase == prworkspace.PhasePublication &&
			aggregate.Workspace.ExecutionState == prworkspace.ExecutionWaitingGate &&
			len(aggregate.Publications) == 0,
		CensorReasons: codingAgentBenchmarkSortedCensors(options.censorReasons),
	}
	if manifest.RemoteUnchanged == false {
		t.Fatalf("benchmark changed bare-origin refs: before %q after %q", options.fixture.remoteRefs, remoteRefs)
	}
	if options.productionSandbox {
		if ciRuntime == nil || ciRuntime.runner == nil || ciRuntime.runner.Sandbox == nil {
			t.Fatal("sandboxed benchmark grader requires the live production LocalCI runtime")
		}
		manifest.Grader = runCodingAgentBenchmarkSandboxedGrader(
			t, cfg, options.fixture, evidence.CandidateDiff, options.artifactRoot,
		)
	} else if options.runGrader {
		manifest.Grader = runCodingAgentBenchmarkGrader(
			t, options.fixture, evidence.CandidateDiff, options.artifactRoot,
		)
	} else if mode == "live" {
		t.Fatal("live benchmark requires a sandboxed hardened grader")
	}
	encodedManifest, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for _, privateIdentity := range []string{defaultAgent.Model, defaultAgent.AccountRef} {
		if privateIdentity != "" && strings.Contains(string(encodedManifest), privateIdentity) {
			t.Fatal("benchmark manifest contains a raw model/account identity")
		}
	}
	if options.artifactRoot != "" {
		writeCodingAgentBenchmarkArtifact(t, options.artifactRoot, "candidate.patch", []byte(evidence.CandidateDiff))
		writeCodingAgentBenchmarkArtifact(t, options.artifactRoot, "manifest-v2.json", append(encodedManifest, '\n'))
	}
	return codingAgentBenchmarkResult{aggregate: aggregate, manifest: manifest, patch: evidence.CandidateDiff}
}

func codingAgentBenchmarkConfig(workspace string) *config.Config {
	cfg := config.DefaultConfig()
	bindCodingAgentBenchmarkWorkspace(cfg, workspace)
	cfg.Agents.Defaults.AccountRef = "benchmark-account"
	cfg.Agents.Defaults.ModelName = "benchmark-model"
	cfg.Agents.Defaults.MaxTokens = 4096
	cfg.Agents.Defaults.MaxToolIterations = 8
	cfg.Agents.List = []config.AgentConfig{{
		ID: codingAgentBenchmarkAgentID, Default: true, AccountRef: "benchmark-account",
		Model: &config.AgentModelConfig{Primary: "benchmark-model"},
	}}
	cfg.ModelAliases = []config.ModelAliasConfig{{Name: "benchmark-model", Model: "benchmark-model"}}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "benchmark-account", Provider: "openai", Model: "benchmark-model",
		APIBase: "http://benchmark.invalid/v1", APIKeys: config.SimpleSecureStrings("benchmark-key"),
		ReasoningEffort: "low", Enabled: true,
	}}
	return cfg
}

func validateCodingAgentBenchmarkLiveProfile(cfg *config.Config) error {
	if cfg == nil {
		return errors.New("configuration is unavailable")
	}
	if err := cfg.ValidateModelSelections(); err != nil {
		return fmt.Errorf("model selection is invalid: %w", err)
	}
	accountRef := strings.TrimSpace(cfg.Agents.Defaults.AccountRef)
	modelAlias := strings.TrimSpace(cfg.Agents.Defaults.ModelName)
	fallbacks := append([]string(nil), cfg.Agents.Defaults.ModelFallbacks...)
	var selected *config.AgentConfig
	for index := range cfg.Agents.List {
		candidate := &cfg.Agents.List[index]
		if !candidate.Default {
			continue
		}
		if selected != nil {
			return errors.New("multiple default agents are configured")
		}
		selected = candidate
	}
	if selected != nil {
		if value := strings.TrimSpace(selected.AccountRef); value != "" {
			accountRef = value
		}
		if selected.Model != nil {
			if value := strings.TrimSpace(selected.Model.Primary); value != "" {
				modelAlias = value
			}
			if selected.Model.Fallbacks != nil {
				fallbacks = append([]string(nil), selected.Model.Fallbacks...)
			}
		}
	}
	if accountRef == "" || modelAlias == "" {
		return errors.New("one exact account and model alias are required")
	}
	if len(fallbacks) != 0 {
		return errors.New("model fallbacks are not allowed")
	}
	if routing := cfg.Agents.Defaults.Routing; routing != nil && routing.Enabled {
		return errors.New("light-model routing is not allowed")
	}
	for index := range cfg.AccountRouters {
		if cfg.AccountRouters[index].Name == accountRef {
			return errors.New("account routers are not allowed")
		}
	}
	for index := range cfg.ModelRouters {
		if cfg.ModelRouters[index].Name == modelAlias {
			return errors.New("model routers are not allowed")
		}
	}
	concreteAccounts := 0
	for _, account := range cfg.ModelList {
		if account == nil || !account.Enabled || account.ModelName != accountRef {
			continue
		}
		concreteAccounts++
		if account.IsAccountRouter() || account.IsModelRouter() || account.IsVirtual() {
			return errors.New("selected account is not one concrete account")
		}
	}
	if concreteAccounts != 1 {
		return fmt.Errorf("selected account has %d enabled concrete entries, want one", concreteAccounts)
	}
	resolved, err := cfg.ResolveModelAliasConfig(modelAlias, accountRef)
	if err != nil {
		return fmt.Errorf("resolve direct account and alias: %w", err)
	}
	if len(resolved.Fallbacks) != 0 || resolved.Router != nil || resolved.ModelRouter != nil {
		return errors.New("resolved model contains a fallback or router")
	}
	provider, _ := providers.ExtractProtocol(resolved)
	if provider != "openai" {
		return fmt.Errorf("resolved provider is %q, want openai", provider)
	}
	effort, err := providercommon.NormalizeReasoningEffort(resolved.ReasoningEffort)
	if err != nil {
		return fmt.Errorf("normalize reasoning effort: %w", err)
	}
	if effort != "low" {
		return fmt.Errorf("reasoning effort is %q, want low", effort)
	}
	return nil
}

func bindCodingAgentBenchmarkWorkspace(cfg *config.Config, workspace string) {
	cfg.Agents.Defaults.Workspace = workspace
	cfg.GitWorkspaces.RootDir = filepath.Join(workspace, "git-workspaces")
	cfg.Tools.MCP.Enabled = false
	cfg.Hooks.Enabled = false
	cfg.Events.Ingress.Enabled = true
	cfg.Events.Ingress.DatabasePath = filepath.Join(workspace, "state", "events.db")
	for _, model := range cfg.ModelList {
		if model != nil && (strings.EqualFold(model.Provider, "codex-cli") ||
			strings.HasPrefix(strings.ToLower(model.Model), "codex-cli/")) {
			model.Workspace = workspace
		}
	}
	for index := range cfg.Agents.List {
		cfg.Agents.List[index].Workspace = workspace
	}
}

func seedCodingAgentBenchmarkService(
	t *testing.T,
	runtimeAdapter *prWorkspaceImplementationRuntime,
	loop *agent.AgentLoop,
	fixture codingAgentBenchmarkFixture,
) (*prworkspace.Service, prworkspace.Aggregate, *codingAgentBenchmarkAIRecorder) {
	t.Helper()
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	workspaceID := "devw_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	provider := prworkspace.ProviderSnapshot{
		Intent: prworkspace.IntentImplementFeature, SourceKind: prworkspace.SourceBrief,
		SourceID: "sha256:" + strings.Repeat("b", 64), Provider: "local-git",
		ProviderOrigin: fixture.originRoot, RepositoryID: "benchmark-repository",
		Repository: "benchmark/" + codingAgentBenchmarkFixtureID,
		Title:      "Implement idempotent ledger transfers",
		Body:       "Implement the bounded transfer task in task.md.",
		BaseRef:    "main", BaseSHA: fixture.head,
		HeadRepositoryID: "benchmark-repository",
		HeadRepository:   "benchmark/" + codingAgentBenchmarkFixtureID,
		HeadRef:          "main", HeadSHA: fixture.head,
		State: "open", Owned: true, HeadWritable: true, CanCreatePullRequest: true,
		ObservedAt: now,
	}
	store := prworkspace.NewMemoryStore()
	_, err := store.Create(t.Context(), prworkspace.CreateInput{
		RequestID: "benchmark-seed-create-0001",
		Workspace: prworkspace.Workspace{
			ID: workspaceID, Intent: provider.Intent, SourceKind: provider.SourceKind,
			SourceID: provider.SourceID, Provider: provider.Provider,
			ProviderOrigin: provider.ProviderOrigin, RepositoryID: provider.RepositoryID,
			Repository: provider.Repository, Phase: prworkspace.PhaseCharter,
			ExecutionState:  prworkspace.ExecutionWaitingUser,
			ProviderHeadSHA: fixture.head, Version: 1, CreatedAt: now, UpdatedAt: now,
		},
		Provider: provider,
	})
	if err != nil {
		t.Fatalf("seed benchmark workspace: %v", err)
	}
	charter := prworkspace.Charter{
		ID: "pcr_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Revision: 1,
		Type: prworkspace.PRTypeFeature,
		Goal: "Implement atomic, idempotent, concurrency-safe ledger transfers.",
		AcceptanceCriteria: []string{
			"Validate request identity before resolving replay precedence.",
			"Preserve balances and request IDs on every failure.",
			"Reject overflow and make concurrent duplicate transfers apply once.",
		},
		IncludedAreas: []string{"ledger"}, ExcludedAreas: []string{"dependencies", "network"},
		NonGoals: []string{"new public APIs"}, BaseSHA: fixture.head, HeadSHA: fixture.head,
		Confirmed: true, CreatedAt: now, ConfirmedAt: &now,
	}
	planning := prworkspace.StageRun{
		ID: "psr_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Stage: "planning",
		State: prworkspace.ExecutionSucceeded, CharterID: charter.ID, HeadSHA: fixture.head,
		Attempt: 1, Summary: "Implement and test the transfer contract.",
		StartedAt: now, FinishedAt: &now,
	}
	finding := prworkspace.Finding{
		ID: "pfn_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Fingerprint: "sha256:" + strings.Repeat("c", 64),
		Origin: prworkspace.FindingOriginReview, OriginRunID: planning.ID,
		Severity: "high", Title: "Transfer is not implemented",
		Message: "Implement every requirement in task.md and add focused tests.",
		Scope: prworkspace.ScopeAssessment{
			Distance: prworkspace.ScopeExact, Size: prworkspace.ChangeSizeS,
			TypeCompatible: true, Confidence: 1,
		},
		Disposition: prworkspace.FindingInScope, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	phase, state, active := prworkspace.PhaseTriage, prworkspace.ExecutionQueued, charter.ID
	seeded, err := store.Mutate(t.Context(), prworkspace.Mutation{
		WorkspaceID: workspaceID, ExpectedVersion: 1, RequestID: "benchmark-seed-plan-0001",
		Patch: prworkspace.AggregatePatch{
			Phase: &phase, ExecutionState: &state, ActiveCharterID: &active,
			AppendCharters:  []prworkspace.Charter{charter},
			AppendStageRuns: []prworkspace.StageRun{planning},
			UpsertFindings:  []prworkspace.Finding{finding},
		},
	})
	if err != nil {
		t.Fatalf("seed benchmark plan: %v", err)
	}
	aiRecorder := &codingAgentBenchmarkAIRecorder{delegate: prworkspace.WorkflowAIRunner{
		Runner: agent.NewWorkflowAgentRunner(loop), AgentID: loop.GetRegistry().GetDefaultAgent().ID,
	}}
	service, err := prworkspace.NewService(prworkspace.ServiceConfig{
		Store: store, CandidateEvidence: runtimeAdapter, PlanningEvidence: runtimeAdapter,
		AI:    aiRecorder,
		Gates: codingAgentBenchmarkGateEvaluator{}, Now: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("create benchmark service: %v", err)
	}
	return service, seeded.Aggregate, aiRecorder
}

type codingAgentBenchmarkAIRecorder struct {
	mu       sync.Mutex
	delegate prworkspace.IsolatedAIRunner
	err      error
}

func (recorder *codingAgentBenchmarkAIRecorder) RunIsolated(
	ctx context.Context,
	request prworkspace.IsolatedAIRequest,
) (prworkspace.IsolatedAIResult, error) {
	result, err := recorder.delegate.RunIsolated(ctx, request)
	recorder.mu.Lock()
	if err != nil {
		recorder.err = err
	}
	recorder.mu.Unlock()
	return result, err
}

func (recorder *codingAgentBenchmarkAIRecorder) lastError() error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.err
}

func assertCodingAgentBenchmarkResult(
	t *testing.T,
	result codingAgentBenchmarkResult,
	provider *codingAgentBenchmarkScriptedProvider,
	wantTwoCycles bool,
) {
	t.Helper()
	if !result.manifest.PublicationStopped || !result.manifest.RemoteUnchanged {
		t.Fatalf("benchmark crossed an effect fence: %#v", result.manifest)
	}
	for _, objectID := range []string{
		result.manifest.ProductCommit, result.manifest.FixtureTree,
		result.manifest.BaseCommit, result.manifest.CandidateCommit, result.manifest.CandidateTree,
	} {
		if !codingAgentBenchmarkGitObjectID(objectID) {
			t.Fatalf("benchmark manifest contains malformed Git provenance %q", objectID)
		}
	}
	for _, digest := range []string{result.manifest.TaskSHA256, result.manifest.HarnessSourceSHA256} {
		if !codingAgentBenchmarkOpaqueDigest(digest) {
			t.Fatalf("benchmark manifest contains malformed source provenance %q", digest)
		}
	}
	if result.manifest.Usage.Scope != prworkspace.ImplementationUsageScope ||
		!result.manifest.Usage.Complete ||
		result.manifest.Usage.Total.ProviderCalls != result.manifest.ProviderCalls ||
		result.manifest.Usage.Total.ProviderCalls == 0 ||
		result.manifest.Usage.Total.UsageReportedCalls != result.manifest.Usage.Total.ProviderCalls {
		t.Fatalf("benchmark usage = %#v", result.manifest.Usage)
	}
	if wantTwoCycles && (result.manifest.RepairAttempts != 2 || result.manifest.ValidationRuns != 2) {
		t.Fatalf(
			"benchmark cycles = repairs %d validations %d",
			result.manifest.RepairAttempts,
			result.manifest.ValidationRuns,
		)
	}
	if len(result.manifest.RepairPromptDigests) != result.manifest.RepairAttempts ||
		len(result.manifest.RepairProfileDigests) != result.manifest.RepairAttempts {
		t.Fatalf("benchmark omitted persisted repair profile evidence: %#v", result.manifest)
	}
	for _, digest := range append(
		append([]string(nil), result.manifest.RepairPromptDigests...),
		result.manifest.RepairProfileDigests...,
	) {
		if !codingAgentBenchmarkOpaqueDigest(digest) {
			t.Fatalf("benchmark manifest contains a malformed repair digest %q", digest)
		}
	}
	encoded, err := json.Marshal(result.manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, rawIdentity := range []string{"benchmark-model", "benchmark-account"} {
		if strings.Contains(string(encoded), rawIdentity) {
			t.Fatalf("benchmark manifest exposed raw identity %q", rawIdentity)
		}
	}
	if result.manifest.Grader == nil ||
		!result.manifest.Grader.MandatoryPass || result.manifest.Grader.Score != 100 {
		t.Fatalf("benchmark grader = %#v", result.manifest.Grader)
	}
	if !strings.Contains(result.patch, "ledger/ledger.go") ||
		!strings.Contains(result.patch, "ledger/ledger_candidate_test.go") {
		t.Fatalf("benchmark patch is incomplete:\n%s", result.patch)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.repairLoops != 2 || provider.toolCalls != 2 || provider.scopeCalls != 2 ||
		provider.completionCalls != 1 || !provider.feedbackObserved ||
		int64(provider.providerCalls) != result.manifest.ProviderCalls {
		t.Fatalf(
			"scripted calls = total:%d repair:%d tool:%d scope:%d completion:%d feedback:%v; manifest=%#v",
			provider.providerCalls, provider.repairLoops, provider.toolCalls,
			provider.scopeCalls, provider.completionCalls, provider.feedbackObserved, result.manifest,
		)
	}
}

func runCodingAgentBenchmarkGrader(
	t *testing.T,
	fixture codingAgentBenchmarkFixture,
	patch string,
	artifactRoot string,
) *codingAgentBenchmarkGrader {
	t.Helper()
	requireCodingAgentBenchmarkGraderDependencies(t)
	checkout := filepath.Join(t.TempDir(), "grader-checkout")
	materializeCodingAgentBenchmarkCandidate(t, fixture, patch, checkout)
	graderOutput := filepath.Join(t.TempDir(), "grader-output")
	command := exec.Command(
		"bash", filepath.Join(fixture.graderRoot, "grade.sh"), checkout, graderOutput, fixture.head,
	)
	command.Env = append(codingAgentBenchmarkCommandEnv(), "GOWORK=off", "GOPROXY=off")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("benchmark grader failed: %v\n%s", err, output)
	}
	raw := readCodingAgentBenchmarkBoundedFile(
		t, filepath.Join(graderOutput, "grader.json"), codingAgentBenchmarkGraderMaxOutput,
	)
	summary, err := decodeCodingAgentBenchmarkGraderArtifact(raw)
	if err != nil {
		t.Fatalf("decode benchmark grader artifact: %v", err)
	}
	if artifactRoot != "" {
		writeCodingAgentBenchmarkArtifact(t, artifactRoot, "grader-v2.json", raw)
	}
	return codingAgentBenchmarkGraderSummary(summary, raw)
}

func runCodingAgentBenchmarkSandboxedGrader(
	t *testing.T,
	cfg *config.Config,
	fixture codingAgentBenchmarkFixture,
	patch string,
	artifactRoot string,
) *codingAgentBenchmarkGrader {
	t.Helper()
	gradingRoot := filepath.Join(t.TempDir(), "sandboxed-grading")
	if err := os.Mkdir(gradingRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	candidateRoot := filepath.Join(gradingRoot, "candidate")
	graderRoot := filepath.Join(gradingRoot, "grader")
	driverRoot := filepath.Join(gradingRoot, "driver")
	materializeCodingAgentBenchmarkCandidate(t, fixture, patch, candidateRoot)
	copyCodingAgentBenchmarkTree(t, fixture.graderRoot, graderRoot)
	if err := os.Mkdir(driverRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{candidateRoot, graderRoot, driverRoot} {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || resolved != path {
			t.Fatalf("sandboxed grader root is not canonical: %q", path)
		}
	}
	temporaryRoot, _, err := preparePRWorkspaceLocalCIState(cfg)
	if err != nil {
		t.Fatalf("prepare sandboxed grader state: %v", err)
	}
	sandbox, err := localci.NewSandbox(localci.SandboxConfig{
		TemporaryRoot: temporaryRoot,
		DependencyMounts: []localci.DependencyMount{
			{
				Source: candidateRoot, Target: "/dependencies/candidate",
				Digest: codingAgentBenchmarkDirectoryDigest(t, candidateRoot),
			},
			{
				Source: graderRoot, Target: "/dependencies/grader",
				Digest: codingAgentBenchmarkDirectoryDigest(t, graderRoot),
			},
		},
	})
	if err != nil {
		t.Fatalf("initialize sandboxed benchmark grader: %v", err)
	}
	step := codingAgentBenchmarkSandboxedGraderStep(fixture.head)
	result, err := sandbox.RunStep(t.Context(), driverRoot, step, localci.Limits{
		StepTimeout:  5 * time.Minute,
		TotalTimeout: 5 * time.Minute,
		OutputBytes:  codingAgentBenchmarkGraderMaxOutput,
	})
	if err != nil || result.Status != localci.StatusPassed || result.ExitCode != 0 ||
		result.OutputTruncated || result.ObservedOutputBytes > codingAgentBenchmarkGraderMaxOutput {
		t.Fatalf("sandboxed benchmark grader = %#v, error = %v", result, err)
	}
	raw := []byte(result.Output)
	summary, err := decodeCodingAgentBenchmarkGraderArtifact(raw)
	if err != nil {
		t.Fatalf("decode sandboxed benchmark grader stdout: %v", err)
	}
	if artifactRoot != "" {
		writeCodingAgentBenchmarkArtifact(t, artifactRoot, "grader-v2.json", append(raw, '\n'))
	}
	return codingAgentBenchmarkGraderSummary(summary, raw)
}

func codingAgentBenchmarkSandboxedGraderStep(expectedCommit string) localci.Step {
	return localci.Step{
		ID: "benchmark-grader-v2", Name: "External coding-agent grader",
		Kind: localci.StepTest, Origin: localci.OriginExplicit,
		Source: "coding-agent-benchmark-v2", Shell: "bash",
		Script: `candidate=/tmp/picoclaw-grader-candidate
output=/tmp/picoclaw-grader-output
test ! -e "$candidate"
test ! -e "$output"
cp -a -- /dependencies/candidate "$candidate"
test -d "$candidate/.git"
exec /bin/bash /dependencies/grader/grade.sh "$candidate" "$output" "$EXPECTED_COMMIT"
`,
		Environment:    []localci.EnvironmentVariable{{Name: "EXPECTED_COMMIT", Value: expectedCommit}},
		TimeoutSeconds: 300, Required: true,
	}
}

func materializeCodingAgentBenchmarkCandidate(
	t *testing.T,
	fixture codingAgentBenchmarkFixture,
	patch string,
	checkout string,
) {
	t.Helper()
	runCodingAgentBenchmarkGit(t, fixture.bareRepository, "clone", "--quiet", fixture.bareRepository, checkout)
	command := exec.Command("git", "-C", checkout, "apply", "--whitespace=nowarn", "-")
	command.Env = codingAgentBenchmarkCommandEnv()
	command.Stdin = strings.NewReader(patch)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("apply benchmark patch for grader: %v: %s", err, output)
	}
}

func requireCodingAgentBenchmarkGraderDependencies(t *testing.T) {
	t.Helper()
	for _, dependency := range []string{"bash", "git", "go", "gofmt", "jq", "realpath", "sha256sum"} {
		if _, err := exec.LookPath(dependency); err != nil {
			t.Fatalf("benchmark grader dependency is unavailable: %s", dependency)
		}
	}
}

func readCodingAgentBenchmarkBoundedFile(t *testing.T, path string, maximum int64) []byte {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 1 || info.Size() > maximum {
		t.Fatalf("benchmark grader artifact is not a bounded regular file: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || int64(len(raw)) != info.Size() {
		t.Fatalf("read benchmark grader artifact: %v", err)
	}
	return raw
}

func decodeCodingAgentBenchmarkGraderArtifact(
	raw []byte,
) (codingAgentBenchmarkGraderArtifact, error) {
	if len(raw) < 1 || len(raw) > codingAgentBenchmarkGraderMaxOutput {
		return codingAgentBenchmarkGraderArtifact{}, errors.New("grader output exceeds its bound")
	}
	var artifact codingAgentBenchmarkGraderArtifact
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&artifact); err != nil {
		return codingAgentBenchmarkGraderArtifact{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return codingAgentBenchmarkGraderArtifact{}, errors.New("grader output contains trailing data")
	}
	if artifact.Version != 2 || artifact.Fixture != codingAgentBenchmarkFixtureID ||
		artifact.Score != 100 || !artifact.MandatoryPass ||
		!artifact.Checks.Format || !artifact.Checks.Vet || !artifact.Checks.Test ||
		!artifact.Checks.Race || !artifact.Checks.Scope || !artifact.Checks.GitHead ||
		!artifact.Checks.TestsChanged || artifact.Mutation.Total != 5 ||
		artifact.Mutation.Killed != artifact.Mutation.Total || artifact.Mutation.Points != 10 ||
		len(artifact.Mutation.Mutants) != artifact.Mutation.Total {
		return codingAgentBenchmarkGraderArtifact{}, errors.New("grader output did not satisfy the hardened contract")
	}
	for _, mutant := range artifact.Mutation.Mutants {
		if strings.TrimSpace(mutant.ID) == "" || !mutant.Killed {
			return codingAgentBenchmarkGraderArtifact{}, errors.New("grader output contains an invalid mutant result")
		}
	}
	if len(artifact.ChangedFiles) < 1 || len(artifact.ChangedFiles) > 100 {
		return codingAgentBenchmarkGraderArtifact{}, errors.New("grader output contains invalid changed-file evidence")
	}
	for _, path := range artifact.ChangedFiles {
		normalized := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
		if path != normalized || strings.ContainsRune(path, '\\') ||
			!strings.HasPrefix(path, "ledger/") || !filepath.IsLocal(filepath.FromSlash(path)) {
			return codingAgentBenchmarkGraderArtifact{}, errors.New(
				"grader output contains out-of-scope changed-file evidence",
			)
		}
	}
	for _, digest := range []string{
		artifact.PatchSHA256, artifact.GraderSHA256,
		artifact.HiddenTestSHA256, artifact.MutantsSHA256,
	} {
		if !codingAgentBenchmarkOpaqueDigest(digest) {
			return codingAgentBenchmarkGraderArtifact{}, errors.New("grader output contains an invalid evidence digest")
		}
	}
	return artifact, nil
}

func codingAgentBenchmarkGraderSummary(
	artifact codingAgentBenchmarkGraderArtifact,
	raw []byte,
) *codingAgentBenchmarkGrader {
	return &codingAgentBenchmarkGrader{
		Version: artifact.Version, Score: artifact.Score, MandatoryPass: artifact.MandatoryPass,
		ArtifactSHA256: codingAgentBenchmarkDigest("grader", string(raw)),
	}
}

func codingAgentBenchmarkRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve benchmark source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("resolve benchmark repository root: %v", err)
	}
	return root
}

func codingAgentBenchmarkHarnessSourceDigest(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve benchmark harness source")
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read benchmark harness source: %v", err)
	}
	return codingAgentBenchmarkDigest("harness-source", string(raw))
}

func copyCodingAgentBenchmarkTree(t *testing.T, source, target string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("benchmark fixture contains symlink %q", relative)
		}
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("benchmark fixture contains non-regular file %q", relative)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, raw, 0o600)
	})
	if err != nil {
		t.Fatalf("copy benchmark fixture: %v", err)
	}
}

func codingAgentBenchmarkDirectoryDigest(t *testing.T, root string) string {
	t.Helper()
	type record struct {
		Path   string `json:"path"`
		Kind   string `json:"kind"`
		Mode   uint32 `json:"mode"`
		Size   int64  `json:"size,omitempty"`
		Digest string `json:"digest,omitempty"`
		Link   string `json:"link,omitempty"`
	}
	records := make([]record, 0, 128)
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || !filepath.IsLocal(relative) {
			return errors.New("benchmark dependency path escaped its root")
		}
		if len(records) >= 100_000 {
			return errors.New("benchmark dependency file count exceeds bound")
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		item := record{Path: filepath.ToSlash(relative), Mode: uint32(info.Mode().Perm())}
		switch {
		case info.IsDir():
			item.Kind = "directory"
		case info.Mode().IsRegular():
			item.Kind, item.Size = "file", info.Size()
			total += info.Size()
			if total > 1<<30 {
				return errors.New("benchmark dependency bytes exceed bound")
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil || int64(len(raw)) != info.Size() {
				return errors.Join(errors.New("read benchmark dependency file"), readErr)
			}
			digest := sha256.Sum256(raw)
			item.Digest = hex.EncodeToString(digest[:])
		case info.Mode()&os.ModeSymlink != 0:
			item.Kind = "symlink"
			item.Link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		default:
			return errors.New("benchmark dependency contains a special file")
		}
		records = append(records, item)
		return nil
	})
	if err != nil {
		t.Fatalf("digest benchmark dependency: %v", err)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })
	encoded, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(append([]byte("picoclaw-benchmark-dependency-v1\x00"), encoded...))
	return hex.EncodeToString(digest[:])
}

func requireCodingAgentBenchmarkGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
}

func runCodingAgentBenchmarkGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	var command *exec.Cmd
	if len(arguments) > 0 && arguments[0] == "clone" {
		command = exec.Command("git", arguments...)
		command.Dir = directory
	} else {
		command = exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	}
	command.Env = codingAgentBenchmarkCommandEnv()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v: %s", strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

func codingAgentBenchmarkCommandEnv() []string {
	return append(
		os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=",
	)
}

func codingAgentBenchmarkOpaqueID(prefix string, values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return prefix + hex.EncodeToString(digest[:16])
}

func codingAgentBenchmarkDigest(domain, value string) string {
	digest := sha256.Sum256([]byte("picoclaw-coding-agent-benchmark-v2\x00" + domain + "\x00" + value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func codingAgentBenchmarkIdentitySalt(t *testing.T, mode string) string {
	t.Helper()
	if mode != "live" {
		return "scripted-fixture-v1"
	}
	value := make([]byte, 16)
	if _, err := cryptorand.Read(value); err != nil {
		t.Fatalf("create live benchmark identity salt: %v", err)
	}
	return hex.EncodeToString(value)
}

func codingAgentBenchmarkIdentityDigest(salt, domain, value string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"picoclaw-coding-agent-benchmark-identity-v1", salt, domain, value,
	}, "\x00")))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func codingAgentBenchmarkOpaqueDigest(value string) bool {
	raw, ok := strings.CutPrefix(value, "sha256:")
	if !ok || len(raw) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(raw)
	return err == nil && len(decoded) == sha256.Size
}

func codingAgentBenchmarkGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func prepareCodingAgentBenchmarkPrivateOutput(t *testing.T, raw string) string {
	t.Helper()
	if !filepath.IsAbs(raw) || filepath.Clean(raw) != raw {
		t.Fatal("live benchmark output must be a canonical absolute path")
	}
	repositoryRoot := codingAgentBenchmarkRepositoryRoot(t)
	resolvedRepositoryRoot, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil || resolvedRepositoryRoot != repositoryRoot {
		t.Fatal("benchmark repository root is not canonical")
	}
	parent := filepath.Dir(raw)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil || resolvedParent != parent {
		t.Fatal("live benchmark output parent must be an existing canonical directory")
	}
	relative, err := filepath.Rel(repositoryRoot, raw)
	if err != nil || relative == "." || filepath.IsLocal(relative) {
		t.Fatal("live benchmark output must be outside the repository")
	}
	if _, err = os.Lstat(raw); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("live benchmark output path must not already exist")
	}
	if err = os.Mkdir(raw, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(raw)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("live benchmark output is not a private real directory")
	}
	if err = os.Chmod(raw, 0o700); err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeCodingAgentBenchmarkArtifact(t *testing.T, root, name string, content []byte) {
	t.Helper()
	path := filepath.Join(root, name)
	if filepath.Dir(path) != root {
		t.Fatal("benchmark artifact name escaped output root")
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Keep stable serialized ordering for future paired-run manifest comparisons.
func codingAgentBenchmarkSortedCensors(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

var (
	_ providers.LLMProvider          = (*codingAgentBenchmarkScriptedProvider)(nil)
	_ prworkspace.ValidationExecutor = (*codingAgentBenchmarkScriptedValidation)(nil)
	_ prworkspace.GateEvaluator      = codingAgentBenchmarkGateEvaluator{}
)
