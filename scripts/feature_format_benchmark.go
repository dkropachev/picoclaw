//go:build featuretools

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type benchmarkCandidate struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Format     string   `json:"format"`
	Iterations []string `json:"iterations"`
	Spec       string   `json:"-"`
}

type commandResult struct {
	Name     string `json:"name"`
	Command  string `json:"command"`
	Passed   bool   `json:"passed"`
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output,omitempty"`
}

type scoreBreakdown struct {
	TestPassRate        int      `json:"test_pass_rate"`
	RequirementCoverage int      `json:"requirement_coverage"`
	APICompatibility    int      `json:"api_compatibility"`
	StateCorrectness    int      `json:"state_correctness"`
	FailureSemantics    int      `json:"failure_semantics"`
	CrossFeature        int      `json:"cross_feature"`
	Maintainability     int      `json:"maintainability"`
	Total               int      `json:"total"`
	Findings            []string `json:"findings,omitempty"`
}

type benchmarkResult struct {
	Candidate benchmarkCandidate `json:"candidate"`
	Worktree  string             `json:"worktree"`
	Commands  []commandResult    `json:"commands"`
	Changed   []string           `json:"changed_files"`
	Score     scoreBreakdown     `json:"score"`
}

var hiddenTestFiles = []string{
	"pkg/events/events_test.go",
	"pkg/events/subscription_test.go",
	"pkg/events/filter_test.go",
	"pkg/config/events_test.go",
}

var targetImplementationGlob = filepath.Join("pkg", "events", "*.go")

func main() {
	defaultOut := filepath.Join(os.TempDir(), "picoclaw-feature-format-benchmark")
	var (
		outDir        = flag.String("out", defaultOut, "output directory")
		runAI         = flag.Bool("run-ai", false, "run Codex regeneration agents")
		model         = flag.String("model", "gpt-5.3-codex-spark", "Codex model")
		effort        = flag.String("effort", "medium", "Codex reasoning effort")
		timeout       = flag.Duration("timeout", 12*time.Minute, "timeout per candidate")
		candidateList = flag.String("candidates", "", "comma-separated candidate IDs to run")
		keepWorktrees = flag.Bool("keep-worktrees", true, "keep benchmark worktrees for inspection")
		scoreExisting = flag.String("score-existing", "", "score an existing generated worktree as candidate-id:path")
	)
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fail("feature format benchmark: %v", err)
	}
	candidates := filterCandidates(allCandidates(), *candidateList)
	if len(candidates) == 0 {
		fail("feature format benchmark: no candidates selected")
	}

	out := *outDir
	if !filepath.IsAbs(out) {
		out = filepath.Join(root, out)
	}
	if err := os.MkdirAll(filepath.Join(out, "specs"), 0o755); err != nil {
		fail("feature format benchmark: create output dir: %v", err)
	}
	for _, candidate := range candidates {
		if err := os.WriteFile(filepath.Join(out, "specs", candidate.ID+".md"), []byte(candidate.Spec), 0o644); err != nil {
			fail("feature format benchmark: write spec %s: %v", candidate.ID, err)
		}
	}
	if err := writeCandidateSummary(out, candidates); err != nil {
		fail("feature format benchmark: write candidate summary: %v", err)
	}
	if *scoreExisting != "" {
		result, err := scoreExistingWorktree(allCandidates(), *scoreExisting)
		if err != nil {
			fail("feature format benchmark: score existing: %v", err)
		}
		if err := writeResults(out, []benchmarkResult{result}); err != nil {
			fail("feature format benchmark: write scored result: %v", err)
		}
		fmt.Printf("scored existing worktree: %s score=%d\n", result.Candidate.ID, result.Score.Total)
		return
	}
	if !*runAI {
		fmt.Printf("wrote %d candidate specs to %s\n", len(candidates), displayPath(root, out))
		return
	}

	var results []benchmarkResult
	for _, candidate := range candidates {
		fmt.Printf("benchmark: running %s\n", candidate.ID)
		result := runCandidate(root, out, candidate, *model, *effort, *timeout, *keepWorktrees)
		results = append(results, result)
		if err := writeResults(out, results); err != nil {
			fail("feature format benchmark: write partial results: %v", err)
		}
		fmt.Printf("benchmark: %s score=%d\n", candidate.ID, result.Score.Total)
	}
	if err := writeResults(out, results); err != nil {
		fail("feature format benchmark: write results: %v", err)
	}
	fmt.Printf("benchmark complete: %s\n", displayPath(root, filepath.Join(out, "results.md")))
}

func runCandidate(root, out string, candidate benchmarkCandidate, model, effort string, timeout time.Duration, keep bool) benchmarkResult {
	worktree := filepath.Join(out, "worktrees", candidate.ID)
	_ = os.RemoveAll(worktree)
	if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
		return benchmarkResult{Candidate: candidate, Worktree: worktree, Score: scoreBreakdown{Findings: []string{err.Error()}}}
	}
	result := benchmarkResult{Candidate: candidate, Worktree: worktree}
	if cmd := runCmd(root, "git", "worktree", "add", "--detach", worktree, "HEAD"); !cmd.Passed {
		result.Commands = append(result.Commands, cmd)
		result.Score = score(result)
		return result
	} else {
		result.Commands = append(result.Commands, cmd)
	}
	if !keep {
		defer func() {
			_ = runCmd(root, "git", "worktree", "remove", "--force", worktree)
		}()
	}

	if err := redactWorktree(root, worktree, candidate.Spec); err != nil {
		result.Score = scoreWithFinding(result, err.Error())
		return result
	}

	promptPath := filepath.Join(worktree, "BENCHMARK_PROMPT.md")
	if err := os.WriteFile(promptPath, []byte(agentPrompt(candidate)), 0o644); err != nil {
		result.Score = scoreWithFinding(result, err.Error())
		return result
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	logPath := filepath.Join(out, "logs", candidate.ID+".codex.log")
	lastPath := filepath.Join(out, "logs", candidate.ID+".last.md")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		result.Score = scoreWithFinding(result, err.Error())
		return result
	}
	codexArgs := []string{
		"exec",
		"--ephemeral",
		"-C", worktree,
		"-s", "workspace-write",
		"-m", model,
		"-c", "model_reasoning_effort=\"" + effort + "\"",
		"--output-last-message", lastPath,
		"-",
	}
	cmd := exec.CommandContext(ctx, "codex", codexArgs...)
	cmd.Stdin = strings.NewReader(agentPrompt(candidate))
	output, err := cmd.CombinedOutput()
	_ = os.WriteFile(logPath, output, 0o644)
	result.Commands = append(result.Commands, commandFromExec("codex regeneration", "codex "+strings.Join(codexArgs, " "), output, err))

	if err := restoreHiddenTests(root, worktree); err != nil {
		result.Score = scoreWithFinding(result, err.Error())
		return result
	}

	result.Commands = append(result.Commands,
		runCmd(worktree, "go", "test", "-tags", "goolm,stdjson", "./pkg/events"),
		runCmd(worktree, "go", "test", "-tags", "goolm,stdjson", "./pkg/config"),
		runCmd(worktree, "go", "test", "-tags", "goolm,stdjson", "./pkg/bus", "./pkg/mcp", "./pkg/gateway"),
		runCmd(worktree, "gofmt", "-l", "pkg/events"),
		runCmd(worktree, "git", "diff", "--name-only"),
	)
	result.Changed = changedFiles(worktree)
	result.Score = score(result)
	return result
}

func scoreExistingWorktree(candidates []benchmarkCandidate, spec string) (benchmarkResult, error) {
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 {
		return benchmarkResult{}, fmt.Errorf("expected candidate-id:path")
	}
	var candidate benchmarkCandidate
	found := false
	for _, item := range candidates {
		if item.ID == parts[0] {
			candidate = item
			found = true
			break
		}
	}
	if !found {
		return benchmarkResult{}, fmt.Errorf("unknown candidate %q", parts[0])
	}
	worktree, err := filepath.Abs(parts[1])
	if err != nil {
		return benchmarkResult{}, err
	}
	result := benchmarkResult{Candidate: candidate, Worktree: worktree}
	result.Commands = append(result.Commands,
		runCmd(worktree, "go", "test", "-tags", "goolm,stdjson", "./pkg/events"),
		runCmd(worktree, "go", "test", "-tags", "goolm,stdjson", "./pkg/config"),
		runCmd(worktree, "go", "test", "-tags", "goolm,stdjson", "./pkg/bus", "./pkg/mcp", "./pkg/gateway"),
		runCmd(worktree, "gofmt", "-l", "pkg/events"),
		runCmd(worktree, "git", "diff", "--name-only"),
	)
	result.Changed = changedFiles(worktree)
	result.Score = score(result)
	return result, nil
}

func redactWorktree(root, worktree, spec string) error {
	matches, err := filepath.Glob(filepath.Join(worktree, targetImplementationGlob))
	if err != nil {
		return err
	}
	for _, match := range matches {
		if strings.HasSuffix(match, "_test.go") {
			continue
		}
		if err := os.Remove(match); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	for _, relPath := range hiddenTestFiles {
		if err := os.Remove(filepath.Join(worktree, relPath)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	_ = os.Remove(filepath.Join(worktree, "docs", "architecture", "runtime-events.md"))
	if err := os.WriteFile(filepath.Join(worktree, "docs", "features", "runtime-events.md"), []byte(spec), 0o644); err != nil {
		return err
	}
	_ = root
	return nil
}

func restoreHiddenTests(root, worktree string) error {
	for _, relPath := range hiddenTestFiles {
		data, err := os.ReadFile(filepath.Join(root, relPath))
		if err != nil {
			return fmt.Errorf("read hidden test %s: %w", relPath, err)
		}
		target := filepath.Join(worktree, relPath)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return fmt.Errorf("restore hidden test %s: %w", relPath, err)
		}
	}
	return nil
}

func agentPrompt(candidate benchmarkCandidate) string {
	return fmt.Sprintf(`You are a benchmark regeneration agent.

Task: recreate the Runtime Events feature implementation from the feature spec.

Benchmark setup:
- The target implementation files under pkg/events were removed.
- The target unit tests were hidden and will be restored after you finish.
- docs/features/runtime-events.md has been replaced with the candidate format named %q.
- You may inspect existing non-hidden repository code to understand imports and public API usage.

Rules:
- Implement only package pkg/events unless a compile error proves a tiny adjacent compatibility edit is required.
- Do not edit tests, docs, Makefile, go.mod, or generated benchmark files.
- Prefer simple idiomatic Go over overfitting.
- Run gofmt on files you create.
- Run at least: go test -tags goolm,stdjson ./pkg/events ./pkg/bus ./pkg/mcp ./pkg/gateway
- Stop when the package compiles and the visible tests you can run pass.

Candidate format spec is in docs/features/runtime-events.md.
`, candidate.Name)
}

func score(result benchmarkResult) scoreBreakdown {
	text := generatedEventsText(result.Worktree)
	findings := []string{}

	tests := map[string]int{
		"go test -tags goolm,stdjson ./pkg/events":                      20,
		"go test -tags goolm,stdjson ./pkg/config":                      5,
		"go test -tags goolm,stdjson ./pkg/bus ./pkg/mcp ./pkg/gateway": 10,
	}
	testScore := 0
	for _, cmd := range result.Commands {
		if weight, ok := tests[cmd.Command]; ok {
			if cmd.Passed {
				testScore += weight
			} else {
				findings = append(findings, cmd.Name+" failed")
			}
		}
	}

	requirements := weightedChecks(text, []weightedCheck{
		wc(4, "FR-EVENTS-001 envelope fields", "type Event struct", "Kind", "Time", "Source", "Scope", "Correlation", "Severity", "Payload", "Attrs"),
		wc(4, "FR-EVENTS-002 subscriptions", "type EventChannel interface", "Subscribe(", "SubscribeChan(", "SubscribeOnce(", "Close() error", "Done() <-chan struct{}"),
		wc(4, "FR-EVENTS-003 filters", "MatchKind", "MatchKindPrefix", "MatchSource", "MatchScope", "And(", "Or("),
		wc(4, "FR-EVENTS-004 known kinds", "KnownKinds", "KindAgentTurnStart", "KindChannelLifecycleStarted", "KindBusPublishFailed", "KindGatewayStart", "KindMCPServerConnected"),
		wc(4, "FR-EVENTS-005 safe payload shape", "Payload", "any", "Attrs", "map[string]any"),
	}, &findings)

	api := weightedChecks(text, []weightedCheck{
		wc(3, "bus interface", "type Bus interface", "Publish(ctx context.Context, evt Event) PublishResult", "PublishNonBlocking(evt Event) PublishResult", "Stats() Stats"),
		wc(2, "publish result", "type PublishResult struct", "Matched", "Delivered", "Dropped", "Blocked", "Closed"),
		wc(2, "severity constants", "SeverityDebug", "SeverityInfo", "SeverityWarn", "SeverityError"),
		wc(2, "subscribe options", "type SubscribeOptions struct", "Buffer", "Priority", "Concurrency", "Backpressure", "Timeout", "PanicPolicy"),
		wc(2, "channel filters", "OfKind", "KindPrefix", "Source(", "Scope("),
		wc(2, "stats API", "type Stats struct", "type SubscriberStats struct", "Published", "SubscriberStats"),
		wc(2, "kind string API", "type Kind string", "func (k Kind) String() string"),
	}, &findings)

	state := weightedChecks(text, []weightedCheck{
		wc(2, "closed bus state", "closed", "ErrBusClosed"),
		wc(2, "subscriber storage", "subs", "orderedSubs"),
		wc(2, "atomic counters", "atomic", "published", "delivered", "dropped"),
		wc(2, "stable event ids", "nextEventID", "evt-"),
		wc(2, "buffer default", "defaultSubscriberBuffer", "Buffer <= 0"),
	}, &findings)

	failures := weightedChecks(text, []weightedCheck{
		wc(2, "nil handler", "ErrNilHandler"),
		wc(2, "backpressure policies", "DropNewest", "DropOldest", "Block"),
		wc(2, "panic policy", "RecoverAndLog", "Crash", "recover"),
		wc(2, "timeout behavior", "Timeout", "context.WithTimeout"),
		wc(2, "nil/closed handling", "nil", "Closed: true"),
	}, &findings)

	cross := 0
	for _, cmd := range result.Commands {
		if cmd.Command == "go test -tags goolm,stdjson ./pkg/bus ./pkg/mcp ./pkg/gateway" && cmd.Passed {
			cross = 5
		}
	}

	maint := 0
	for _, cmd := range result.Commands {
		if cmd.Command == "gofmt -l pkg/events" {
			if strings.TrimSpace(cmd.Output) == "" {
				maint += 2
			} else {
				findings = append(findings, "gofmt reported files")
			}
		}
	}
	outside := outsideTargetChanges(result.Changed)
	if len(outside) == 0 {
		maint += 2
	} else {
		findings = append(findings, "changed outside pkg/events: "+strings.Join(outside, ", "))
	}
	if eventFileCount(result.Worktree) > 0 && eventFileCount(result.Worktree) <= 12 {
		maint++
	}

	total := testScore + requirements + api + state + failures + cross + maint
	return scoreBreakdown{
		TestPassRate:        testScore,
		RequirementCoverage: requirements,
		APICompatibility:    api,
		StateCorrectness:    state,
		FailureSemantics:    failures,
		CrossFeature:        cross,
		Maintainability:     maint,
		Total:               total,
		Findings:            findings,
	}
}

func scoreWithFinding(result benchmarkResult, finding string) scoreBreakdown {
	score := score(result)
	score.Findings = append(score.Findings, finding)
	return score
}

type weightedCheck struct {
	Weight int
	Name   string
	Terms  []string
}

func wc(weight int, name string, terms ...string) weightedCheck {
	return weightedCheck{Weight: weight, Name: name, Terms: terms}
}

func weightedChecks(text string, checks []weightedCheck, findings *[]string) int {
	score := 0
	for _, check := range checks {
		ok := true
		for _, term := range check.Terms {
			if !strings.Contains(text, term) {
				ok = false
				break
			}
		}
		if ok {
			score += check.Weight
		} else {
			*findings = append(*findings, "missing "+check.Name)
		}
	}
	return score
}

func generatedEventsText(worktree string) string {
	var b strings.Builder
	_ = filepath.WalkDir(filepath.Join(worktree, "pkg", "events"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err == nil {
			b.Write(data)
			b.WriteByte('\n')
		}
		return nil
	})
	return b.String()
}

func eventFileCount(worktree string) int {
	count := 0
	_ = filepath.WalkDir(filepath.Join(worktree, "pkg", "events"), func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			count++
		}
		return nil
	})
	return count
}

func outsideTargetChanges(changed []string) []string {
	var outside []string
	for _, path := range changed {
		if strings.HasPrefix(path, "pkg/events/") || path == "docs/features/runtime-events.md" || path == "docs/architecture/runtime-events.md" || path == "BENCHMARK_PROMPT.md" {
			continue
		}
		if contains(hiddenTestFiles, path) {
			continue
		}
		outside = append(outside, path)
	}
	sort.Strings(outside)
	return outside
}

func changedFiles(worktree string) []string {
	cmd := runCmd(worktree, "git", "diff", "--name-only")
	lines := strings.Fields(cmd.Output)
	sort.Strings(lines)
	return lines
}

func runCmd(dir string, name string, args ...string) commandResult {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
	output, err := cmd.CombinedOutput()
	return commandFromExec(name+" "+strings.Join(args, " "), name+" "+strings.Join(args, " "), output, err)
}

func commandFromExec(name, command string, output []byte, err error) commandResult {
	result := commandResult{Name: name, Command: command, Output: trimOutput(output), Passed: err == nil}
	if err == nil {
		return result
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
	} else {
		result.ExitCode = -1
	}
	return result
}

func trimOutput(output []byte) string {
	const limit = 12000
	output = bytes.TrimSpace(output)
	if len(output) <= limit {
		return string(output)
	}
	return string(output[:limit]) + "\n...[truncated]..."
}

func writeCandidateSummary(out string, candidates []benchmarkCandidate) error {
	var b strings.Builder
	b.WriteString("# Feature Format Benchmark Candidates\n\n")
	b.WriteString("Target feature: `FR-EVENTS` runtime events. Each candidate has three explicit improvement iterations before its final spec is tested.\n\n")
	for _, candidate := range candidates {
		fmt.Fprintf(&b, "## %s\n\n", candidate.Name)
		fmt.Fprintf(&b, "- ID: `%s`\n", candidate.ID)
		fmt.Fprintf(&b, "- Format: %s\n", candidate.Format)
		for i, iteration := range candidate.Iterations {
			fmt.Fprintf(&b, "- Iteration %d: %s\n", i+1, iteration)
		}
		b.WriteString("\n")
	}
	return os.WriteFile(filepath.Join(out, "candidates.md"), []byte(b.String()), 0o644)
}

func writeResults(out string, results []benchmarkResult) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "results.json"), data, 0o644); err != nil {
		return err
	}

	sorted := append([]benchmarkResult(nil), results...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Score.Total != sorted[j].Score.Total {
			return sorted[i].Score.Total > sorted[j].Score.Total
		}
		return sorted[i].Candidate.ID < sorted[j].Candidate.ID
	})

	var b strings.Builder
	b.WriteString("# Feature Format Benchmark Results\n\n")
	b.WriteString("| Rank | Candidate | Total | Tests | Req | API | State | Failures | Cross | Maint |\n")
	b.WriteString("| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for i, result := range sorted {
		s := result.Score
		fmt.Fprintf(&b, "| %d | `%s` | %d | %d | %d | %d | %d | %d | %d | %d |\n",
			i+1, result.Candidate.ID, s.Total, s.TestPassRate, s.RequirementCoverage, s.APICompatibility, s.StateCorrectness, s.FailureSemantics, s.CrossFeature, s.Maintainability)
	}
	b.WriteString("\n## Findings\n\n")
	for _, result := range sorted {
		fmt.Fprintf(&b, "### %s\n\n", result.Candidate.Name)
		if len(result.Score.Findings) == 0 {
			b.WriteString("- No scoring findings.\n\n")
			continue
		}
		for _, finding := range result.Score.Findings {
			fmt.Fprintf(&b, "- %s\n", finding)
		}
		b.WriteString("\n")
	}
	return os.WriteFile(filepath.Join(out, "results.md"), []byte(b.String()), 0o644)
}

func filterCandidates(candidates []benchmarkCandidate, list string) []benchmarkCandidate {
	if strings.TrimSpace(list) == "" {
		return candidates
	}
	wanted := map[string]bool{}
	for _, id := range strings.Split(list, ",") {
		wanted[strings.TrimSpace(id)] = true
	}
	var out []benchmarkCandidate
	for _, candidate := range candidates {
		if wanted[candidate.ID] {
			out = append(out, candidate)
		}
	}
	return out
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func displayPath(root, path string) string {
	relPath, err := filepath.Rel(root, path)
	if err == nil && relPath != "." && !strings.HasPrefix(relPath, ".."+string(filepath.Separator)) && relPath != ".." {
		return relPath
	}
	return path
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func allCandidates() []benchmarkCandidate {
	return []benchmarkCandidate{
		userStoryCandidate(),
		apiFirstCandidate(),
		bddCandidate(),
		contractMatrixCandidate(),
		domainModelCandidate(),
		testFirstCandidate(),
		stateMachineCandidate(),
		promptPackCandidate(),
		implementationBlueprintCandidate(),
		behavioralReconstructionCandidate(),
	}
}

func userStoryCandidate() benchmarkCandidate {
	return benchmarkCandidate{
		ID:     "user-story-acceptance-v4",
		Name:   "User Story Acceptance Contract v4",
		Format: "Actors, user stories, acceptance criteria, and implementation hints.",
		Iterations: []string{
			"Started with actor stories and broad acceptance criteria.",
			"Added explicit public API names after stories were too vague for code generation.",
			"Added state, error, and hidden-test reconstruction hints while preserving story framing.",
		},
		Spec: spec("User Story Acceptance Contract", `
## Actors

- Runtime publishers emit agent, channel, bus, gateway, and MCP events.
- Subscribers consume filtered events through handlers or channels.
- Operators use logging filters for diagnostics without mutating publication.

## User Stories

| ID | Story | Acceptance |
| --- | --- | --- |
| FR-EVENTS-001 | As a publisher, I publish an event envelope with stable metadata. | Event contains ID, kind, time, source, scope, correlation, severity, payload, and attrs where supplied. Missing ID/time are filled at publish time. |
| FR-EVENTS-002 | As a subscriber, I filter event delivery and receive close signals. | Bus exposes Channel, filters by kind/source/scope, supports handler/channel/once subscriptions, and closes all subscriptions when bus closes. |
| FR-EVENTS-003 | As an operator, I filter logs without suppressing bus publication. | Filters are pure predicates; logging config lives outside pkg/events and must not affect Publish. |
| FR-EVENTS-004 | As an integrator, I use stable event kind constants. | KnownKinds returns a copy containing agent, channel, bus, gateway, and MCP kind constants. |
| FR-EVENTS-005 | As a security reviewer, I avoid raw secret leakage. | Payload is optional any; attrs map carries safe summaries/counts; filters do not inspect secrets by default. |

## Implementation Hints
`+runtimeEventsAPIFacts()+`

## Done When

- Hidden pkg/events tests pass.
- Existing bus, mcp, gateway packages compile and pass against the regenerated package.
- Only pkg/events implementation files are changed.
`),
	}
}

func apiFirstCandidate() benchmarkCandidate {
	return benchmarkCandidate{
		ID:     "api-first-contract-v4",
		Name:   "API First Contract v4",
		Format: "Public symbols, method signatures, enum values, and compile contracts first.",
		Iterations: []string{
			"Started with endpoint-style public symbol inventory.",
			"Added behavior expectations beside each symbol after signature-only specs produced stubs.",
			"Added state counters, error semantics, and event-kind completeness checks.",
		},
		Spec: spec("API First Contract", `
## Public Go API

`+runtimeEventsAPIFacts()+`

## Behavioral Semantics

`+runtimeEventsSemantics()+`

## Compatibility Tests

- `+"`go test -tags goolm,stdjson ./pkg/events`"+`
- `+"`go test -tags goolm,stdjson ./pkg/config`"+`
- `+"`go test -tags goolm,stdjson ./pkg/bus ./pkg/mcp ./pkg/gateway`"+`
`),
	}
}

func bddCandidate() benchmarkCandidate {
	return benchmarkCandidate{
		ID:     "bdd-scenarios-v4",
		Name:   "BDD Scenario Contract v4",
		Format: "Given/when/then scenarios with API appendix.",
		Iterations: []string{
			"Started with Given/When/Then scenarios only.",
			"Added scenario examples for backpressure, close, once, panic, and timeout.",
			"Added API appendix because scenarios alone under-specified symbols and constants.",
		},
		Spec: spec("BDD Scenario Contract", `
## Scenarios

1. Given a new bus and a channel subscriber of kind `+"`agent.turn.start`"+`, when that event is published, then the subscriber receives it with generated ID/time if missing.
2. Given a subscriber with unmatched kind/source/scope filters, when events are published, then delivery counters do not increase for that subscriber.
3. Given a channel subscription, when the bus closes, then the subscription delivery channel and Done channel close.
4. Given a full queue with DropNewest, when PublishNonBlocking is called, then the new event is dropped and counters record it.
5. Given a full queue with DropOldest, when PublishNonBlocking is called, then one queued event is discarded and the new event is queued.
6. Given a full queue with Block, when Publish is called with a canceling context, then delivery reports blocked/dropped according to timeout/cancel behavior.
7. Given SubscribeOnce, when the first event is handled, then the subscription closes.
8. Given a panicking handler and RecoverAndLog, when an event is handled, then the worker records panic without crashing the bus.
9. Given a handler timeout, when the handler exceeds Timeout, then timed-out stats increment.
10. Given KnownKinds, when callers mutate the returned slice, then package state is unchanged.

## API Appendix

`+runtimeEventsAPIFacts()+`

## Behavior Appendix

`+runtimeEventsSemantics()+`
`),
	}
}

func contractMatrixCandidate() benchmarkCandidate {
	return benchmarkCandidate{
		ID:     "contract-matrix-v4",
		Name:   "Contract Matrix v4",
		Format: "Dense matrix of trigger, state, output, failures, evidence.",
		Iterations: []string{
			"Started with requirement rows only.",
			"Added input/state/output/failure columns to remove ambiguity.",
			"Added implementation-signature appendix so matrix rows map to compilable Go.",
		},
		Spec: spec("Contract Matrix", `
## Behavior Contract Matrix

| Requirement | Trigger | Required State | Output | Failure/Edge |
| --- | --- | --- | --- | --- |
| FR-EVENTS-001 | Publish Event with missing ID/time | Atomic global event sequence and current clock | Event delivered with non-empty ID and Time | Closed/nil bus returns Closed result |
| FR-EVENTS-002 | Subscribe and publish matching event | Ordered subscriber list, buffered queue, filters | Matched/delivered counters and event delivery | Close unsubscribes and closes channels |
| FR-EVENTS-003 | Apply filters/log config | Pure filter functions and external config defaults | Include/exclude decisions only affect logging caller | Empty filters match all, invalid patterns should not broaden unexpectedly |
| FR-EVENTS-004 | Call KnownKinds | Internal knownKinds slice | Defensive copy of all stable constants | Caller mutation cannot alter internal slice |
| FR-EVENTS-005 | Publish payload/attrs | Optional Payload and Attrs map | Safe envelope can carry summaries/counts | No package-level secret expansion |

## API Contract

`+runtimeEventsAPIFacts()+`

## Algorithms

`+runtimeEventsSemantics()+`
`),
	}
}

func domainModelCandidate() benchmarkCandidate {
	return benchmarkCandidate{
		ID:     "domain-model-v4",
		Name:   "Domain Model Contract v4",
		Format: "Entities, invariants, transitions, and API mapping.",
		Iterations: []string{
			"Started with entities and relationships.",
			"Added invariants for counters, close state, queues, and defensive copies.",
			"Mapped every entity back to concrete public Go symbols.",
		},
		Spec: spec("Domain Model Contract", `
## Domain Entities

- Event: immutable envelope after Publish fills ID/time.
- Kind: stable string category with String method and KnownKinds registry.
- Source: component plus optional name.
- Scope: runtime, agent, session, turn, channel, chat, topic, space, sender, and message identity.
- Correlation: trace, parent turn, request, and reply identity.
- Bus: synchronized publisher with subscriber registry and aggregate stats.
- EventChannel: filtered view over one bus.
- Subscription: queue plus lifecycle, handler worker, counters, and Done signal.
- Filter: pure predicate over Event.

## Invariants

- Publish never mutates logging config and logging config never suppresses bus publication.
- Subscriber ordering is priority descending, ID ascending.
- KnownKinds returns a copy.
- Close is idempotent and closes every subscription input path.
- Default SubscribeOptions are buffer 16, Locked, DropNewest, RecoverAndLog.

## API Mapping

`+runtimeEventsAPIFacts()+`

## State Transitions

`+runtimeEventsSemantics()+`
`),
	}
}

func testFirstCandidate() benchmarkCandidate {
	return benchmarkCandidate{
		ID:     "test-first-contract-v4",
		Name:   "Test First Contract v4",
		Format: "Expected hidden tests and visible compatibility checks.",
		Iterations: []string{
			"Started by naming test classes and visible packages.",
			"Added expected assertions for each hidden test family.",
			"Added API and state notes so implementation is not just test stubbing.",
		},
		Spec: spec("Test First Contract", `
## Hidden Test Families To Satisfy

- Event envelope tests: ID generation, time defaulting, kind string conversion, severity constants, source/scope/correlation JSON fields.
- Filter tests: MatchKind, MatchKindPrefix, MatchSource, MatchScope, And, Or, empty filters, nil filters.
- Bus tests: publish result counts, nonblocking delivery, closed bus, nil bus, generated event IDs.
- Subscription tests: default options, channel subscribe, handler subscribe, once subscribe, close semantics, priority order, backpressure policies, panic recovery, timeout stats.
- KnownKinds tests: complete list, defensive copy, required domain prefixes.

## Visible Compatibility Tests

- Bus, MCP, and gateway packages must compile against pkg/events without edits.
- Config event logging defaults remain in pkg/config and do not move into pkg/events.

## API Required By Tests

`+runtimeEventsAPIFacts()+`

## Behavioral Required By Tests

`+runtimeEventsSemantics()+`
`),
	}
}

func stateMachineCandidate() benchmarkCandidate {
	return benchmarkCandidate{
		ID:     "state-machine-v4",
		Name:   "State Machine Contract v4",
		Format: "State machines for bus, subscription, and handler processing.",
		Iterations: []string{
			"Started with lifecycle states.",
			"Added transition guards for close, publish, queue full, and context cancel.",
			"Added public API and constants needed to instantiate transitions.",
		},
		Spec: spec("State Machine Contract", `
## Bus State Machine

- New: no subscribers, counters zero, closed false.
- Active: subscribers can be added, publish snapshots ordered subscribers and delivers matching events.
- Closing: Close swaps closed state under lock and closes subscription inputs outside the lock.
- Closed: Subscribe returns ErrBusClosed, Publish returns PublishResult{Closed:true}, Close is no-op.

## Subscription State Machine

- Created: options normalized, queue allocated, done open.
- Running: handler subscription consumes queue; channel subscription exposes queue directly.
- Closing: Close removes from bus and closes input once.
- Done: handler worker drains/settles and closes Done once.

## Handler State Machine

- Locked/Keyed: process synchronously in subscription worker.
- Concurrent: spawn per event with panic guard.
- Timeout: wrap context, record TimedOut when handler exceeds Timeout.
- Once: close after first dispatch.

## API And Constants

`+runtimeEventsAPIFacts()+`

## Delivery Rules

`+runtimeEventsSemantics()+`
`),
	}
}

func promptPackCandidate() benchmarkCandidate {
	return benchmarkCandidate{
		ID:     "agent-prompt-pack-v4",
		Name:   "Agent Prompt Pack v4",
		Format: "Direct coding prompt with must-build and must-not-build rules.",
		Iterations: []string{
			"Started as a direct implementation prompt.",
			"Added no-edit boundaries and exact compatibility commands.",
			"Added detailed API, state, and edge-case checklists.",
		},
		Spec: spec("Agent Prompt Pack", `
## Task Prompt

Create package `+"`pkg/events`"+` for runtime event publication and subscription.

## Must Build

`+runtimeEventsAPIFacts()+`

## Must Behave

`+runtimeEventsSemantics()+`

## Must Not Build

- Do not move event logging config out of pkg/config.
- Do not use global mutable subscriber state except the event ID sequence.
- Do not edit bus, mcp, gateway, agent, or config packages to compensate for missing API.
- Do not expose target test-only helpers.

## Verification Commands

- `+"`go test -tags goolm,stdjson ./pkg/events`"+`
- `+"`go test -tags goolm,stdjson ./pkg/config`"+`
- `+"`go test -tags goolm,stdjson ./pkg/bus ./pkg/mcp ./pkg/gateway`"+`
`),
	}
}

func implementationBlueprintCandidate() benchmarkCandidate {
	return benchmarkCandidate{
		ID:     "implementation-blueprint-v4",
		Name:   "Implementation Blueprint v4",
		Format: "Recommended files, types, algorithms, and tests.",
		Iterations: []string{
			"Started with package/file layout.",
			"Added exact type signatures and algorithm order.",
			"Added edge-case and cross-package compatibility constraints.",
		},
		Spec: spec("Implementation Blueprint", `
## Suggested File Layout

- `+"`types.go`"+`: Kind, Event, Source, Scope, Correlation, Severity.
- `+"`kind.go`"+`: stable kind constants and KnownKinds defensive copy.
- `+"`filter.go`"+`: Filter type and match/composition helpers.
- `+"`bus.go`"+`: Bus interface, EventBus implementation, PublishResult, ID generation, stats snapshot.
- `+"`channel.go`"+`: EventChannel filtered view and subscribe methods.
- `+"`subscription.go`"+`: SubscribeOptions, policies, Subscription implementation, handler processing.
- `+"`stats.go`"+`: Stats and SubscriberStats.
- `+"`doc.go`"+`: package docs.

## Type And Function Blueprint

`+runtimeEventsAPIFacts()+`

## Algorithm Blueprint

`+runtimeEventsSemantics()+`
`),
	}
}

func behavioralReconstructionCandidate() benchmarkCandidate {
	return benchmarkCandidate{
		ID:     "behavioral-reconstruction-contract-v4",
		Name:   "Behavioral Reconstruction Contract v4",
		Format: "Goal, exact public surface, behavior matrix, state model, algorithms, failures, evidence.",
		Iterations: []string{
			"Started with the current hybrid requirements format.",
			"Added exact public surface contracts and behavior matrix columns.",
			"Added reconstruction target, state invariants, algorithm order, and scoring/evidence hooks.",
		},
		Spec: spec("Behavioral Reconstruction Contract", `
## Reconstruction Target

Recreate the Runtime Events implementation so generated code is API-compatible
with existing bus, MCP, gateway, agent, and config packages and behavior-compatible
with hidden pkg/events tests.

## Behavior Contract Matrix

| ID | Trigger/Input | Required output | State mutation | Edge behavior |
| --- | --- | --- | --- | --- |
| FR-EVENTS-001 | Publish Event with Kind/Source/Scope/Severity/Payload/Attrs | Event reaches matching subscribers with ID/time filled if missing | Published/delivered counters update | Nil or closed bus reports closed |
| FR-EVENTS-002 | Create EventChannel filters and subscribe | Matching events reach handler or channel; once closes after first event | Subscriber registry, ordered list, per-subscriber counters | Close is idempotent and closes delivery |
| FR-EVENTS-003 | Compose filters/logging caller decisions | Predicates match kind, prefix, source, scope, And, Or | No publication state is changed by filters | Empty/nil filters match all safely |
| FR-EVENTS-004 | Call KnownKinds | Copy of all stable kind constants | No caller mutation leaks back | Agent/channel/bus/gateway/MCP domains all present |
| FR-EVENTS-005 | Carry payload and attrs | Optional payload/attrs are available to subscribers | No package-level secret expansion | Prefer summaries/counts in callers |

## Public Surface Contract

`+runtimeEventsAPIFacts()+`

## State Model And Algorithms

`+runtimeEventsSemantics()+`

## Cross-Feature Compatibility

- Message bus publishes bus failure/drop/close events through the Bus interface.
- MCP manager publishes server and tool events through the Bus interface.
- Gateway publishes lifecycle events through the Bus interface.
- Runtime event logging consumes events but must not alter delivery.

## Acceptance Evidence

- Hidden pkg/events tests evaluate envelope, filters, bus, subscription, stats, and known kind behavior.
- Visible packages `+"`pkg/bus`"+`, `+"`pkg/mcp`"+`, and `+"`pkg/gateway`"+` evaluate cross-feature API compatibility.
`),
	}
}

func spec(title, body string) string {
	return strings.TrimSpace(`# Runtime Events And Observability

## Format

`+title+`

## Feature ID

`+"`FR-EVENTS`"+`

## Behavior Summary

Runtime events provide observable envelopes for agent, channel, gateway, bus,
and MCP behavior. Event delivery is independent from logging; logging filters
may decide what is printed, but publication still reaches subscribers.

`+strings.TrimSpace(body)+`
`) + "\n"
}

func runtimeEventsAPIFacts() string {
	return strings.TrimSpace(`
### Event Envelope

- ` + "`type Kind string`" + ` with ` + "`func (k Kind) String() string`" + `.
- ` + "`type Event struct`" + ` fields: ` + "`ID string`" + `, ` + "`Kind Kind`" + `, ` + "`Time time.Time`" + `, ` + "`Source Source`" + `, ` + "`Scope Scope`" + `, ` + "`Correlation Correlation`" + `, ` + "`Severity Severity`" + `, ` + "`Payload any`" + `, ` + "`Attrs map[string]any`" + `.
- ` + "`type Source struct`" + `: ` + "`Component string`" + `, ` + "`Name string`" + `.
- ` + "`type Scope struct`" + `: ` + "`RuntimeID`" + `, ` + "`AgentID`" + `, ` + "`SessionKey`" + `, ` + "`TurnID`" + `, ` + "`Channel`" + `, ` + "`Account`" + `, ` + "`ChatID`" + `, ` + "`TopicID`" + `, ` + "`SpaceID`" + `, ` + "`SpaceType`" + `, ` + "`ChatType`" + `, ` + "`SenderID`" + `, ` + "`MessageID`" + `.
- ` + "`type Correlation struct`" + `: ` + "`TraceID`" + `, ` + "`ParentTurnID`" + `, ` + "`RequestID`" + `, ` + "`ReplyToID`" + `.
- ` + "`type Severity string`" + ` constants: ` + "`SeverityDebug`" + `=` + "`debug`" + `, ` + "`SeverityInfo`" + `=` + "`info`" + `, ` + "`SeverityWarn`" + `=` + "`warn`" + `, ` + "`SeverityError`" + `=` + "`error`" + `.

### Bus And Channels

- ` + "`type Bus interface`" + ` has ` + "`Publish(context.Context, Event) PublishResult`" + `, ` + "`PublishNonBlocking(Event) PublishResult`" + `, ` + "`Channel() EventChannel`" + `, ` + "`Close() error`" + `, ` + "`Stats() Stats`" + `.
- ` + "`type EventBus`" + ` implements Bus; ` + "`NewBus() *EventBus`" + ` creates it.
- ` + "`type PublishResult struct`" + ` fields: ` + "`Matched`" + `, ` + "`Delivered`" + `, ` + "`Dropped`" + `, ` + "`Blocked`" + `, ` + "`Closed`" + `.
- ` + "`type EventChannel interface`" + ` has ` + "`Filter`" + `, ` + "`OfKind`" + `, ` + "`KindPrefix`" + `, ` + "`Source`" + `, ` + "`Scope`" + `, ` + "`Subscribe`" + `, ` + "`SubscribeChan`" + `, ` + "`SubscribeOnce`" + `.

### Filters

- ` + "`type Filter func(Event) bool`" + `.
- ` + "`type ScopeFilter`" + ` includes ` + "`AgentID`" + `, ` + "`SessionKey`" + `, ` + "`TurnID`" + `, ` + "`Channel`" + `, ` + "`ChatID`" + `, ` + "`MessageID`" + `.
- Filter constructors: ` + "`MatchKind`" + `, ` + "`MatchKindPrefix`" + `, ` + "`MatchSource`" + `, ` + "`MatchScope`" + `, ` + "`And`" + `, ` + "`Or`" + `.

### Subscriptions And Stats

- Errors: ` + "`ErrBusClosed`" + `, ` + "`ErrNilHandler`" + `.
- ` + "`type Handler func(context.Context, Event) error`" + `.
- ` + "`type SubscribeOptions`" + ` fields: ` + "`Name`" + `, ` + "`Buffer`" + `, ` + "`Priority`" + `, ` + "`Concurrency`" + `, ` + "`Backpressure`" + `, ` + "`Timeout`" + `, ` + "`PanicPolicy`" + `.
- Concurrency values: ` + "`Concurrent`" + `, ` + "`Locked`" + `, ` + "`Keyed`" + `.
- Backpressure values: ` + "`DropNewest`" + `, ` + "`DropOldest`" + `, ` + "`Block`" + `.
- Panic values: ` + "`RecoverAndLog`" + `, ` + "`Crash`" + `.
- ` + "`type Subscription interface`" + `: ` + "`ID`" + `, ` + "`Name`" + `, ` + "`Close`" + `, ` + "`Done`" + `, ` + "`Stats`" + `.
- ` + "`type Stats`" + ` fields: ` + "`Published`" + `, ` + "`Matched`" + `, ` + "`Delivered`" + `, ` + "`Dropped`" + `, ` + "`Blocked`" + `, ` + "`Closed`" + `, ` + "`Subscribers`" + `, ` + "`SubscriberStats`" + `.
- ` + "`type SubscriberStats`" + ` fields: ` + "`ID`" + `, ` + "`Name`" + `, ` + "`Received`" + `, ` + "`Handled`" + `, ` + "`Failed`" + `, ` + "`Dropped`" + `, ` + "`Panicked`" + `, ` + "`TimedOut`" + `.

### Known Kinds

- Agent: ` + "`KindAgentTurnStart`" + `, ` + "`KindAgentTurnEnd`" + `, ` + "`KindAgentLLMRequest`" + `, ` + "`KindAgentLLMDelta`" + `, ` + "`KindAgentLLMResponse`" + `, ` + "`KindAgentLLMRetry`" + `, ` + "`KindAgentContextCompress`" + `, ` + "`KindAgentSessionSummarize`" + `, ` + "`KindAgentToolExecStart`" + `, ` + "`KindAgentToolExecEnd`" + `, ` + "`KindAgentToolExecSkipped`" + `, ` + "`KindAgentSteeringInjected`" + `, ` + "`KindAgentFollowUpQueued`" + `, ` + "`KindAgentInterruptReceived`" + `, ` + "`KindAgentSubTurnSpawn`" + `, ` + "`KindAgentSubTurnEnd`" + `, ` + "`KindAgentSubTurnResultDelivered`" + `, ` + "`KindAgentSubTurnOrphan`" + `, ` + "`KindAgentError`" + `.
- Channel: ` + "`KindChannelLifecycleStarted`" + `, ` + "`KindChannelLifecycleInitialized`" + `, ` + "`KindChannelLifecycleStartFailed`" + `, ` + "`KindChannelLifecycleStopped`" + `, ` + "`KindChannelWebhookRegistered`" + `, ` + "`KindChannelWebhookUnregistered`" + `, ` + "`KindChannelMessageOutboundQueued`" + `, ` + "`KindChannelMessageOutboundSent`" + `, ` + "`KindChannelMessageOutboundFailed`" + `, ` + "`KindChannelRateLimited`" + `.
- Bus: ` + "`KindBusPublishFailed`" + `, ` + "`KindBusMessageDropped`" + `, ` + "`KindBusCloseStarted`" + `, ` + "`KindBusCloseCompleted`" + `, ` + "`KindBusCloseDrained`" + `.
- Gateway: ` + "`KindGatewayStart`" + `, ` + "`KindGatewayReady`" + `, ` + "`KindGatewayShutdown`" + `, ` + "`KindGatewayReloadStarted`" + `, ` + "`KindGatewayReloadCompleted`" + `, ` + "`KindGatewayReloadFailed`" + `.
- MCP: ` + "`KindMCPServerConnected`" + `, ` + "`KindMCPServerConnecting`" + `, ` + "`KindMCPServerFailed`" + `, ` + "`KindMCPToolDiscovered`" + `, ` + "`KindMCPToolCallStart`" + `, ` + "`KindMCPToolCallEnd`" + `.
- ` + "`KnownKinds() []Kind`" + ` returns a defensive copy.
`)
}

func runtimeEventsSemantics() string {
	return strings.TrimSpace(`
- Publish defaults nil context to Background, fills missing Time with current time, and fills missing ID with monotonically increasing ` + "`evt-<n>`" + `.
- Publish snapshots subscribers while holding read lock, then delivers outside mutation of the registry.
- Subscribers match only when every non-nil filter returns true.
- Subscriber sort order is Priority descending, then subscription ID ascending.
- SubscribeOptions defaults: Buffer 16, Locked concurrency, DropNewest backpressure, RecoverAndLog panic policy.
- Subscribe rejects nil handler for handler subscriptions but SubscribeChan uses the queue directly and returns ` + "`(<subscription>, <-chan Event, nil)`" + `.
- DropNewest drops a new event when the queue is full. DropOldest removes one queued event and enqueues the new one. Block waits for capacity until Publish context is canceled.
- Concurrent handlers run per event in goroutines; Locked and Keyed process sequentially.
- Handler errors increment Failed; panics increment Panicked under RecoverAndLog; timeouts increment TimedOut.
- SubscribeOnce closes itself after the first dispatched event.
- Close is idempotent, marks the bus closed, removes subscribers, closes subscription input, and eventually closes Done.
- Stats returns aggregate bus counters plus one SubscriberStats entry per active subscriber.
- Nil bus Publish returns Closed true; nil bus Close is nil-safe.
`)
}
