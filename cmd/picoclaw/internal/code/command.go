package code

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/workflows"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

var (
	errAttentionDeferred       = errors.New("development attention deferred")
	errInvalidGateResponse     = errors.New("invalid_gate_response")
	errRetryableCreateResponse = fmt.Errorf("retryable create response: %w", ErrInvalidResponse)
)

const (
	pollInterval       = time.Second
	firstRetryDelay    = 250 * time.Millisecond
	maximumRetryDelay  = 4 * time.Second
	maximumOutageDelay = 30 * time.Second
)

type workspaceClient interface {
	Capabilities(ctx context.Context) (Capabilities, error)
	ListRepositories(ctx context.Context) ([]prworkspace.ConfiguredRepository, error)
	ResolveRepository(
		ctx context.Context,
		repositoryURL string,
	) (prworkspace.ConfiguredRepository, error)
	Create(ctx context.Context, request CreateRequest) (prworkspace.Aggregate, error)
	Get(ctx context.Context, workspaceID string) (prworkspace.Aggregate, error)
	ConfirmCharter(
		ctx context.Context,
		request ConfirmCharterRequest,
	) (prworkspace.Aggregate, error)
	RespondGate(ctx context.Context, request RespondGateRequest) (prworkspace.Aggregate, error)
	ReconcilePublication(
		ctx context.Context,
		request ReconcilePublicationRequest,
	) (prworkspace.Aggregate, error)
}

type commandDependencies struct {
	newClient         func() (workspaceClient, error)
	resolveRepository func(context.Context, string) (string, error)
	random            io.Reader
	sleep             func(context.Context, time.Duration) error
	stdinIsTerminal   func() bool
}

func defaultCommandDependencies() commandDependencies {
	return commandDependencies{
		newClient: func() (workspaceClient, error) {
			return NewClient()
		},
		resolveRepository: resolveRepository,
		random:            rand.Reader,
		sleep:             sleepContext,
		stdinIsTerminal: func() bool {
			return term.IsTerminal(int(os.Stdin.Fd()))
		},
	}
}

type commandOptions struct {
	repository string
	resume     string
	requestID  string
	json       bool
}

// ExitError carries the process outcome after the command has already emitted
// its bounded result. Root must not render a second error panel.
type ExitError struct {
	code int
}

func (failure *ExitError) Error() string {
	return "picoclaw code did not complete"
}

func (failure *ExitError) ExitCode() int {
	if failure == nil || failure.code < 1 {
		return 1
	}
	return failure.code
}

func (*ExitError) CLIErrorHandled() bool {
	return true
}

// IsJSONInvocation detects the machine-readable code-command surface before
// root prints its banner or timezone diagnostics.
func IsJSONInvocation(arguments []string) bool {
	codeIndex := codeCommandIndex(arguments)
	if codeIndex < 0 {
		return false
	}
	jsonMode := false
	helpMode := false
	for _, argument := range arguments[codeIndex+1:] {
		if argument == "--" {
			break
		}
		if argument == "--help" || argument == "-h" {
			helpMode = true
			continue
		}
		if argument == "--json" {
			jsonMode = true
			continue
		}
		name, raw, found := strings.Cut(argument, "=")
		if !found || name != "--json" && name != "--help" && name != "-h" {
			continue
		}
		value, err := strconv.ParseBool(raw)
		if err == nil {
			if name == "--json" {
				jsonMode = value
			} else {
				helpMode = value
			}
		}
	}
	return jsonMode && !helpMode
}

// IsCodeInvocation detects only the root code subcommand. Root uses this to
// scope signal cancellation without mistaking a nested positional "code" for
// this command.
func IsCodeInvocation(arguments []string) bool {
	return codeCommandIndex(arguments) >= 0
}

func codeCommandIndex(arguments []string) int {
	for index, argument := range arguments {
		if index == 0 {
			continue
		}
		if argument == "--" {
			return -1
		}
		if argument == "--no-color" || strings.HasPrefix(argument, "--no-color=") {
			continue
		}
		if strings.HasPrefix(argument, "-") {
			continue
		}
		if argument == "code" {
			return index
		}
		return -1
	}
	return -1
}

// NewCodeCommand creates the gated draft-PR command.
func NewCodeCommand() *cobra.Command {
	return newCodeCommand(defaultCommandDependencies())
}

func newCodeCommand(dependencies commandDependencies) *cobra.Command {
	options := commandOptions{}
	command := &cobra.Command{
		Use:   "code <task>",
		Short: "Implement a task and open a validated draft pull request",
		Long: "Run one durable, human-gated Development workspace through the local Gateway. " +
			"The caller checkout is never modified.",
		Args:          cobra.ArbitraryArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(command *cobra.Command, arguments []string) error {
			runner := commandRunner{
				dependencies: dependencies,
				options:      options,
				arguments:    arguments,
				input:        command.InOrStdin(),
				output:       command.OutOrStdout(),
				errors:       command.ErrOrStderr(),
			}
			return runner.run(command.Context())
		},
	}
	command.Flags().
		StringVar(&options.repository, "repo", "", "Repository path, owner/repo, or GitHub URL")
	command.Flags().
		StringVar(&options.resume, "resume", "", "Resume an existing development workspace")
	command.Flags().
		StringVar(&options.requestID, "request-id", "", "Stable devq_ request ID for create recovery")
	command.Flags().BoolVar(&options.json, "json", false, "Emit exactly one bounded JSON result")
	return command
}

type commandRunner struct {
	dependencies commandDependencies
	options      commandOptions
	arguments    []string
	input        io.Reader
	output       io.Writer
	errors       io.Writer
	client       workspaceClient
	reader       *bufio.Reader
	interactive  bool
	requestID    string
	workspaceID  string
	last         Result
	reconciled   map[string]int
}

func (runner *commandRunner) run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if runner.dependencies.newClient == nil || runner.dependencies.resolveRepository == nil ||
		runner.dependencies.random == nil || runner.dependencies.sleep == nil ||
		runner.dependencies.stdinIsTerminal == nil {
		return runner.finish(Result{Version: resultVersion, ErrorCode: "client_unavailable"}, 1)
	}
	runner.interactive = !runner.options.json && runner.dependencies.stdinIsTerminal()
	runner.reader = bufio.NewReaderSize(runner.input, (64<<10)+1)
	runner.reconciled = make(map[string]int)

	resume, task, validationResult, valid := runner.validateInput()
	if !valid {
		return runner.finish(validationResult, 1)
	}
	var err error
	if !resume {
		runner.requestID, err = runner.createRequestID()
		if err != nil {
			return runner.finish(runner.failureResult("secure_random_unavailable"), 1)
		}
		_, _ = fmt.Fprintf(runner.errors, "request_id=%s\n", runner.requestID)
	}
	client, err := runner.dependencies.newClient()
	if err != nil || client == nil {
		return runner.finish(Result{Version: resultVersion, ErrorCode: "client_unavailable"}, 1)
	}
	runner.client = client

	var aggregate prworkspace.Aggregate
	if resume {
		runner.workspaceID = runner.options.resume
		aggregate, err = runner.getWithRetry(ctx, runner.workspaceID)
		if err != nil {
			return runner.finish(
				runner.failureResult(runner.contextErrorCode(ctx, errorCode(err))),
				runner.contextExit(ctx, 1),
			)
		}
	} else {
		capabilities, capabilityErr := runner.capabilitiesWithRetry(ctx)
		if capabilityErr != nil {
			return runner.finish(
				runner.failureResult(runner.contextErrorCode(ctx, errorCode(capabilityErr))),
				runner.contextExit(ctx, 1),
			)
		}
		if capabilityErr = validateCapabilities(capabilities); capabilityErr != nil {
			return runner.finish(runner.failureResult("malformed_response"), 1)
		}
		if !capabilities.ImplementFeatureReady {
			code := "implementation_unavailable"
			for _, missing := range capabilities.Missing {
				if missing == "unsafe_provider" {
					code = "unsafe_provider"
				}
			}
			return runner.finish(runner.failureResult(code), 1)
		}
		repositoryURL, repositoryErr := runner.dependencies.resolveRepository(ctx, runner.options.repository)
		if repositoryErr != nil {
			return runner.finish(
				runner.failureResult(runner.contextErrorCode(ctx, "repository_invalid")),
				runner.contextExit(ctx, 1),
			)
		}
		configured, repositoryErr := runner.resolveConfiguredRepository(ctx, repositoryURL)
		if repositoryErr != nil {
			return runner.finish(
				runner.failureResult(runner.contextErrorCode(ctx, errorCode(repositoryErr))),
				runner.contextExit(ctx, 1),
			)
		}
		aggregate, err = runner.createWithRetry(ctx, CreateRequest{
			RequestID: runner.requestID, RepositoryIdentity: configured.Identity, Content: task,
		})
		if err != nil {
			return runner.finish(
				runner.failureResult(runner.contextErrorCode(ctx, errorCode(err))),
				runner.contextExit(ctx, 1),
			)
		}
		runner.workspaceID = aggregate.Workspace.ID
		aggregate, err = runner.getWithRetry(ctx, runner.workspaceID)
		if err != nil {
			return runner.finish(
				runner.failureResult(runner.contextErrorCode(ctx, errorCode(err))),
				runner.contextExit(ctx, 1),
			)
		}
	}

	return runner.follow(ctx, aggregate)
}

func (runner *commandRunner) validateInput() (bool, string, Result, bool) {
	result := Result{Version: resultVersion}
	if runner.options.resume != "" {
		if !developmentIDPattern.MatchString(runner.options.resume) || len(runner.arguments) != 0 ||
			runner.options.repository != "" || runner.options.requestID != "" {
			result.ErrorCode = "invalid_request"
			return false, "", result, false
		}
		return true, "", result, true
	}
	if len(runner.arguments) != 1 {
		result.ErrorCode = "invalid_request"
		return false, "", result, false
	}
	task := runner.arguments[0]
	if task == "" || task != strings.TrimSpace(task) || !utf8.ValidString(task) ||
		len(task) > clientMaxTaskBytes || strings.ContainsRune(task, '\x00') {
		result.ErrorCode = "invalid_request"
		return false, "", result, false
	}
	if runner.options.requestID != "" && !validCreateRequestID(runner.options.requestID) {
		result.ErrorCode = "invalid_request"
		return false, "", result, false
	}
	return false, task, result, true
}

func (runner *commandRunner) createRequestID() (string, error) {
	if runner.options.requestID != "" {
		return runner.options.requestID, nil
	}
	raw := make([]byte, 16)
	if _, err := io.ReadFull(runner.dependencies.random, raw); err != nil {
		return "", err
	}
	return "devq_" + hex.EncodeToString(raw), nil
}

func validCreateRequestID(value string) bool {
	if len(value) != len("devq_")+32 || !strings.HasPrefix(value, "devq_") {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "devq_"))
	return err == nil && len(decoded) == 16 && value == strings.ToLower(value)
}

func validateCapabilities(capabilities Capabilities) error {
	if capabilities.Version != 1 || capabilities.Missing == nil ||
		capabilities.ImplementFeatureReady != (len(capabilities.Missing) == 0) {
		return ErrInvalidResponse
	}
	seen := make(map[string]bool, len(capabilities.Missing))
	for _, missing := range capabilities.Missing {
		if missing == "" || len(missing) > 64 || seen[missing] {
			return ErrInvalidResponse
		}
		for _, character := range missing {
			if (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') && character != '_' {
				return ErrInvalidResponse
			}
		}
		seen[missing] = true
	}
	return nil
}

func (runner *commandRunner) resolveConfiguredRepository(
	ctx context.Context,
	repositoryURL string,
) (prworkspace.ConfiguredRepository, error) {
	resolved, err := retryValue(ctx, runner.dependencies.sleep, func(callCtx context.Context) (
		prworkspace.ConfiguredRepository,
		error,
	) {
		return runner.client.ResolveRepository(callCtx, repositoryURL)
	})
	if err != nil {
		return prworkspace.ConfiguredRepository{}, normalizeRepositoryError(err)
	}
	if resolved.Identity == "" || resolved.Name == "" || !resolved.CanImplement {
		return prworkspace.ConfiguredRepository{}, errors.New("repository_not_configured")
	}
	wantedName := strings.TrimPrefix(repositoryURL, "https://github.com/")
	if !validClientBoundedText(resolved.Identity, 1024, false) ||
		!validClientBoundedText(resolved.Name, 1024, false) ||
		!strings.EqualFold(resolved.Name, wantedName) {
		return prworkspace.ConfiguredRepository{}, ErrInvalidResponse
	}
	repositories, err := retryValue(ctx, runner.dependencies.sleep, runner.client.ListRepositories)
	if err != nil {
		return prworkspace.ConfiguredRepository{}, normalizeRepositoryError(err)
	}
	matches := 0
	for _, repository := range repositories {
		if repository.Identity == resolved.Identity && repository.CanImplement {
			matches++
		}
	}
	if matches != 1 {
		return prworkspace.ConfiguredRepository{}, errors.New("repository_not_configured")
	}
	return resolved, nil
}

func (runner *commandRunner) follow(ctx context.Context, aggregate prworkspace.Aggregate) error {
	lastPhase := prworkspace.Phase("")
	lastVersion := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return runner.finish(runner.failureResult("interrupted"), 130)
		}
		if err := validateAggregateBinding(aggregate, runner.workspaceID, ""); err != nil {
			return runner.finish(runner.failureResult("malformed_response"), 1)
		}
		snapshot, err := classifyAggregate(runner.requestID, aggregate)
		if err != nil {
			return runner.finish(runner.failureResult("malformed_response"), 1)
		}
		runner.last = snapshot.result
		if !runner.options.json && (aggregate.Workspace.Phase != lastPhase ||
			aggregate.Workspace.Version != lastVersion) {
			_, _ = fmt.Fprintf(
				runner.output,
				"workspace=%s phase=%s status=%s version=%d\n",
				aggregate.Workspace.ID,
				aggregate.Workspace.Phase,
				aggregate.Workspace.ExecutionState,
				aggregate.Workspace.Version,
			)
			lastPhase, lastVersion = aggregate.Workspace.Phase, aggregate.Workspace.Version
		}
		switch snapshot.action {
		case actionComplete:
			return runner.finish(snapshot.result, 0)
		case actionFail:
			return runner.finish(snapshot.result, 1)
		case actionGate:
			if !runner.interactive {
				snapshot.result.ErrorCode = "human_gate_required"
				return runner.finish(snapshot.result, 2)
			}
			aggregate, err = runner.answerGate(ctx, aggregate, *snapshot.gate)
		case actionCharter:
			if !runner.interactive {
				snapshot.result.ErrorCode = "charter_confirmation_required"
				if snapshot.charter.ClarificationNeeded {
					snapshot.result.ErrorCode = "charter_clarification_required"
				}
				return runner.finish(snapshot.result, 2)
			}
			aggregate, err = runner.confirmCharter(ctx, aggregate, *snapshot.charter)
		case actionReconcile:
			aggregate, err = runner.reconcilePublication(ctx, aggregate, *snapshot.publication)
		case actionPoll:
			if err = runner.dependencies.sleep(ctx, pollInterval); err == nil {
				aggregate, err = runner.getWithRetry(ctx, aggregate.Workspace.ID)
			}
		}
		if err != nil {
			if errors.Is(err, errAttentionDeferred) {
				snapshot.result.ErrorCode = "human_attention_required"
				return runner.finish(snapshot.result, 2)
			}
			if ctx.Err() != nil {
				return runner.finish(runner.failureResult("interrupted"), 130)
			}
			return runner.finish(runner.failureResult(errorCode(err)), 1)
		}
	}
}

func (runner *commandRunner) confirmCharter(
	ctx context.Context,
	aggregate prworkspace.Aggregate,
	charter prworkspace.Charter,
) (prworkspace.Aggregate, error) {
	runner.renderCharter(charter)
	question := "Accept this charter as-is? [y/N]: "
	if charter.ClarificationNeeded && charter.ClarificationQuestion != "" {
		_, _ = fmt.Fprintln(runner.output, boundedTerminalText(charter.ClarificationQuestion, 4096))
	}
	accepted, err := runner.askBoolean(ctx, question)
	if err != nil || !accepted {
		return aggregate, errAttentionDeferred
	}
	request := ConfirmCharterRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		ExpectedCharterRevision: charter.Revision,
		RequestID: mutationRequestID(
			runner.requestID,
			"charter",
			charter.ID,
			aggregate.Workspace.Version,
		),
	}
	return runner.confirmWithRecovery(ctx, request)
}

func (runner *commandRunner) renderCharter(charter prworkspace.Charter) {
	_, _ = fmt.Fprintf(runner.output, "Charter type: %s\n", boundedTerminalText(string(charter.Type), 128))
	_, _ = fmt.Fprintf(runner.output, "Goal: %s\n", boundedTerminalText(charter.Goal, 4096))
	runner.renderCharterList("Acceptance criteria", charter.AcceptanceCriteria)
	runner.renderCharterList("Included areas", charter.IncludedAreas)
	runner.renderCharterList("Excluded areas", charter.ExcludedAreas)
	runner.renderCharterList("Non-goals", charter.NonGoals)
	if value := boundedTerminalText(charter.BaseSHA, 256); value != "" {
		_, _ = fmt.Fprintf(runner.output, "Base revision: %s\n", value)
	}
	if value := boundedTerminalText(charter.HeadSHA, 256); value != "" {
		_, _ = fmt.Fprintf(runner.output, "Head revision: %s\n", value)
	}
}

func (runner *commandRunner) renderCharterList(label string, values []string) {
	_, _ = fmt.Fprintf(runner.output, "%s:\n", label)
	for _, value := range values[:min(len(values), 50)] {
		_, _ = fmt.Fprintf(runner.output, "  - %s\n", boundedTerminalText(value, 1024))
	}
}

func (runner *commandRunner) answerGate(
	ctx context.Context,
	aggregate prworkspace.Aggregate,
	gate prworkspace.GateRun,
) (prworkspace.Aggregate, error) {
	form := latestWaitingGateForm(gate)
	if form == nil {
		return aggregate, ErrInvalidResponse
	}
	runner.renderGateEvidence(gate.Evidence)
	_, _ = fmt.Fprintln(runner.output, boundedTerminalText(form.Prompt, 4096))
	values := make(map[string]any, len(form.Fields))
	for _, field := range form.Fields {
		value, present, err := runner.readGateField(ctx, field)
		if err != nil {
			if errors.Is(err, errAttentionDeferred) {
				return aggregate, err
			}
			return aggregate, errInvalidGateResponse
		}
		if present {
			values[field.ID] = value
		}
	}
	normalized, err := workflows.ValidateGateFieldValues(form.Fields, values)
	if err != nil {
		return aggregate, errInvalidGateResponse
	}
	request := RespondGateRequest{
		WorkspaceID: aggregate.Workspace.ID, GateID: gate.ID,
		ExpectedVersion: aggregate.Workspace.Version,
		RequestID: mutationRequestID(
			runner.requestID,
			"gate",
			gate.ID,
			aggregate.Workspace.Version,
		),
		FieldValues: normalized,
	}
	return runner.respondGateWithRecovery(ctx, request)
}

func (runner *commandRunner) renderGateEvidence(evidence prworkspace.GateEvidence) {
	if value := boundedTerminalText(evidence.CandidateSHA, 256); value != "" {
		_, _ = fmt.Fprintf(runner.output, "candidate=%s\n", value)
	}
	for _, path := range evidence.ChangedFiles[:min(len(evidence.ChangedFiles), 20)] {
		_, _ = fmt.Fprintf(runner.output, "changed=%s\n", boundedTerminalText(path, 512))
	}
	if evidence.ValidationState != "" {
		_, _ = fmt.Fprintf(
			runner.output,
			"validation=%s\n",
			boundedTerminalText(string(evidence.ValidationState), 128),
		)
	}
	if evidence.FindingCount > 0 {
		_, _ = fmt.Fprintf(runner.output, "findings=%d\n", evidence.FindingCount)
	}
	if evidence.PublicationKind != "" {
		_, _ = fmt.Fprintf(
			runner.output,
			"publication=%s\n",
			boundedTerminalText(string(evidence.PublicationKind), 128),
		)
	}
	if value := boundedTerminalText(evidence.Repository, 512); value != "" {
		_, _ = fmt.Fprintf(runner.output, "repository=%s\n", value)
	}
}

func latestWaitingGateForm(gate prworkspace.GateRun) *prworkspace.GateForm {
	for index := len(gate.Turns) - 1; index >= 0; index-- {
		if gate.Turns[index].Status == "waiting" && gate.Turns[index].GateForm != nil {
			return gate.Turns[index].GateForm
		}
	}
	return nil
}

func (runner *commandRunner) readGateField(
	ctx context.Context,
	field gatetypes.GateField,
) (any, bool, error) {
	if !gateFieldTypeSupported(field.Type) {
		return nil, false, errors.New("unsupported gate field")
	}
	if len(field.Options) > 0 {
		for _, option := range field.Options {
			_, _ = fmt.Fprintf(
				runner.output,
				"  %s: %s\n",
				boundedTerminalText(option.ID, 128),
				boundedTerminalText(option.Label, 512),
			)
		}
	}
	_, _ = fmt.Fprintf(runner.output, "%s: ", boundedTerminalText(field.Label, 512))
	line, err := runner.readLine(ctx)
	if err != nil {
		return nil, false, err
	}
	if line == "" && !field.Required &&
		!(field.Type == gatetypes.GateFieldSelect && field.MinSelections > 0) {
		return nil, false, nil
	}
	switch field.Type {
	case gatetypes.GateFieldShortText, gatetypes.GateFieldLongText:
		if len(line) > 64<<10 {
			return nil, false, errors.New("gate response exceeds limit")
		}
		return line, true, nil
	case gatetypes.GateFieldBoolean:
		switch strings.ToLower(line) {
		case "y", "yes", "true":
			return true, true, nil
		case "n", "no", "false":
			return false, true, nil
		default:
			return nil, false, errors.New("gate boolean is invalid")
		}
	case gatetypes.GateFieldSelect:
		if field.MaxSelections == 1 {
			return line, true, nil
		}
		parts := strings.Split(line, ",")
		selected := make([]string, 0, len(parts))
		for _, part := range parts {
			selected = append(selected, strings.TrimSpace(part))
		}
		return selected, true, nil
	default:
		return nil, false, errors.New("unsupported gate field")
	}
}

func (runner *commandRunner) askBoolean(ctx context.Context, prompt string) (bool, error) {
	_, _ = fmt.Fprint(runner.output, prompt)
	line, err := runner.readLine(ctx)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func (runner *commandRunner) readLine(ctx context.Context) (string, error) {
	type lineResult struct {
		line string
		err  error
	}
	result := make(chan lineResult, 1)
	go func() {
		raw, err := runner.reader.ReadSlice('\n')
		result <- lineResult{line: string(raw), err: err}
	}()
	var value lineResult
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case value = <-result:
	}
	if errors.Is(value.err, bufio.ErrBufferFull) || len(value.line) > 64<<10 {
		return "", errors.New("terminal input exceeds limit")
	}
	if value.err != nil && !errors.Is(value.err, io.EOF) {
		return "", value.err
	}
	if errors.Is(value.err, io.EOF) && len(value.line) == 0 {
		return "", errAttentionDeferred
	}
	line := value.line
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	if !utf8.ValidString(line) {
		return "", errors.New("terminal input is invalid")
	}
	return line, nil
}

func (runner *commandRunner) reconcilePublication(
	ctx context.Context,
	aggregate prworkspace.Aggregate,
	publication prworkspace.Publication,
) (prworkspace.Aggregate, error) {
	attempt := runner.reconciled[publication.ID]
	if attempt >= 2 {
		return aggregate, errors.New("publication_outcome_unknown")
	}
	if attempt == 1 && !publicationRecheckObserved(aggregate.Gates, publication.ID) {
		if err := runner.dependencies.sleep(ctx, pollInterval); err != nil {
			return aggregate, err
		}
		return runner.getWithRetry(ctx, aggregate.Workspace.ID)
	}
	runner.reconciled[publication.ID] = attempt + 1
	headRevision := aggregate.ProviderSnapshot.ProviderRevision
	if headRevision == "" {
		headRevision = aggregate.ProviderSnapshot.HeadSHA
	}
	request := ReconcilePublicationRequest{
		WorkspaceID: aggregate.Workspace.ID, PublicationID: publication.ID,
		ExpectedVersion: aggregate.Workspace.Version, ExpectedHeadRevision: headRevision,
		RequestID: mutationRequestID(
			runner.requestID,
			"reconcile"+fmt.Sprint(attempt+1),
			publication.ID,
			aggregate.Workspace.Version,
		),
	}
	return runner.reconcileWithRecovery(ctx, request)
}

func publicationRecheckObserved(values []prworkspace.GateRun, publicationID string) bool {
	for index := len(values) - 1; index >= 0; index-- {
		gate := values[index]
		if gate.DecisionPoint != "pr.publication.reconcile" ||
			gate.TargetID != publicationID || gate.State != prworkspace.ExecutionSucceeded {
			continue
		}
		for turnIndex := len(gate.Turns) - 1; turnIndex >= 0; turnIndex-- {
			action, _ := gate.Turns[turnIndex].FieldValues["action"].(string)
			if action != "" {
				return action == "recheck-provider"
			}
		}
		return false
	}
	return false
}

func mutationRequestID(base, operation, target string, version int64) string {
	value := strings.Join([]string{base, operation, target, fmt.Sprint(version)}, ":")
	if len(value) <= 128 {
		return value
	}
	sum := sha256.Sum256([]byte("picoclaw-code-mutation-v1\x00" + value))
	return "devmut_" + hex.EncodeToString(sum[:16])
}

func (runner *commandRunner) capabilitiesWithRetry(ctx context.Context) (Capabilities, error) {
	return retryValue(ctx, runner.dependencies.sleep, runner.client.Capabilities)
}

func (runner *commandRunner) getWithRetry(
	ctx context.Context,
	workspaceID string,
) (prworkspace.Aggregate, error) {
	return retryValue(
		ctx,
		runner.dependencies.sleep,
		func(callCtx context.Context) (prworkspace.Aggregate, error) {
			aggregate, err := runner.client.Get(callCtx, workspaceID)
			if err == nil {
				err = validateAggregateBinding(aggregate, workspaceID, "")
			} else if current := aggregateFromAPIError(err); current != nil &&
				validateAggregateBinding(*current, workspaceID, "") != nil {
				err = ErrInvalidResponse
			}
			return aggregate, err
		},
	)
}

func (runner *commandRunner) createWithRetry(
	ctx context.Context,
	request CreateRequest,
) (prworkspace.Aggregate, error) {
	malformedResponses := 0
	return retryValue(
		ctx,
		runner.dependencies.sleep,
		func(callCtx context.Context) (prworkspace.Aggregate, error) {
			aggregate, err := runner.client.Create(callCtx, request)
			if err == nil {
				if bindingErr := validateAggregateBinding(
					aggregate,
					"",
					request.RepositoryIdentity,
				); bindingErr != nil {
					malformedResponses++
					if malformedResponses == 1 {
						err = errRetryableCreateResponse
					} else {
						err = ErrInvalidResponse
					}
				}
			} else if current := aggregateFromAPIError(err); current != nil &&
				validateAggregateBinding(*current, "", request.RepositoryIdentity) != nil {
				err = ErrInvalidResponse
			}
			return aggregate, err
		},
	)
}

func retryValue[T any](
	ctx context.Context,
	sleep func(context.Context, time.Duration) error,
	call func(context.Context) (T, error),
) (T, error) {
	var zero T
	retryCtx, cancel := context.WithTimeout(ctx, maximumOutageDelay)
	defer cancel()
	delay := firstRetryDelay
	started := time.Now()
	for {
		value, err := call(retryCtx)
		if err == nil {
			return value, nil
		}
		elapsed := time.Since(started)
		if ctx.Err() != nil || !transientError(err) || elapsed >= maximumOutageDelay {
			return zero, err
		}
		wait := min(delay, maximumOutageDelay-elapsed)
		if sleepErr := sleep(retryCtx, wait); sleepErr != nil {
			return zero, sleepErr
		}
		delay = min(delay*2, maximumRetryDelay)
	}
}

func transientError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return false
	}
	if RequestMayHaveBeenSent(err) {
		return true
	}
	if errors.Is(err, errRetryableCreateResponse) {
		return true
	}
	if errors.Is(err, ErrInvalidRequest) || errors.Is(err, ErrInvalidResponse) ||
		errors.Is(err, ErrClientUnavailable) {
		return false
	}
	return true
}

func (runner *commandRunner) confirmWithRecovery(
	ctx context.Context,
	request ConfirmCharterRequest,
) (prworkspace.Aggregate, error) {
	return runner.mutationWithRecovery(
		ctx,
		request.WorkspaceID,
		request.ExpectedVersion,
		func(callCtx context.Context) (
			prworkspace.Aggregate,
			error,
		) {
			return runner.client.ConfirmCharter(callCtx, request)
		},
	)
}

func (runner *commandRunner) respondGateWithRecovery(
	ctx context.Context,
	request RespondGateRequest,
) (prworkspace.Aggregate, error) {
	return runner.mutationWithRecovery(
		ctx,
		request.WorkspaceID,
		request.ExpectedVersion,
		func(callCtx context.Context) (
			prworkspace.Aggregate,
			error,
		) {
			return runner.client.RespondGate(callCtx, request)
		},
	)
}

func (runner *commandRunner) reconcileWithRecovery(
	ctx context.Context,
	request ReconcilePublicationRequest,
) (prworkspace.Aggregate, error) {
	return runner.mutationWithRecovery(
		ctx,
		request.WorkspaceID,
		request.ExpectedVersion,
		func(callCtx context.Context) (
			prworkspace.Aggregate,
			error,
		) {
			return runner.client.ReconcilePublication(callCtx, request)
		},
	)
}

func (runner *commandRunner) mutationWithRecovery(
	ctx context.Context,
	workspaceID string,
	expectedVersion int64,
	mutate func(context.Context) (prworkspace.Aggregate, error),
) (prworkspace.Aggregate, error) {
	retryCtx, cancel := context.WithTimeout(ctx, maximumOutageDelay)
	defer cancel()
	delay := firstRetryDelay
	started := time.Now()
	for {
		aggregate, err := mutate(retryCtx)
		if err == nil {
			if bindingErr := validateAggregateBinding(aggregate, workspaceID, ""); bindingErr != nil {
				return prworkspace.Aggregate{}, bindingErr
			}
			return aggregate, nil
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			if apiErr.Current != nil {
				if bindingErr := validateAggregateBinding(
					*apiErr.Current,
					workspaceID,
					"",
				); bindingErr != nil ||
					apiErr.Current.Workspace.Version < expectedVersion {
					return prworkspace.Aggregate{}, ErrInvalidResponse
				}
				return *apiErr.Current, nil
			}
			return prworkspace.Aggregate{}, err
		}
		elapsed := time.Since(started)
		if ctx.Err() != nil || !transientError(err) || elapsed >= maximumOutageDelay {
			return prworkspace.Aggregate{}, err
		}
		if RequestMayHaveBeenSent(err) {
			current, getErr := runner.getWithRetry(retryCtx, workspaceID)
			if getErr != nil {
				return prworkspace.Aggregate{}, getErr
			}
			if current.Workspace.Version != expectedVersion {
				return current, nil
			}
		}
		wait := min(delay, maximumOutageDelay-elapsed)
		if sleepErr := runner.dependencies.sleep(retryCtx, wait); sleepErr != nil {
			return prworkspace.Aggregate{}, sleepErr
		}
		delay = min(delay*2, maximumRetryDelay)
	}
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (runner *commandRunner) finish(result Result, exitCode int) error {
	if result.Version == 0 {
		result.Version = resultVersion
	}
	if result.RequestID == "" {
		result.RequestID = runner.requestID
	}
	if result.WorkspaceID == "" {
		result.WorkspaceID = runner.workspaceID
	}
	if result.Usage.Scope == "" {
		result.Usage.Scope = ImplementationUsageScope
	}
	if runner.options.json {
		_ = json.NewEncoder(runner.output).Encode(result)
	} else {
		_, _ = fmt.Fprintf(
			runner.output,
			"result status=%s phase=%s workspace=%s request=%s",
			boundedTerminalText(result.Status, 128),
			boundedTerminalText(result.Phase, 128),
			boundedTerminalText(result.WorkspaceID, 128),
			boundedTerminalText(result.RequestID, 128),
		)
		if result.PullRequestURL != "" {
			_, _ = fmt.Fprintf(runner.output, " pull_request=%s", result.PullRequestURL)
		}
		if result.ErrorCode != "" {
			_, _ = fmt.Fprintf(runner.output, " error=%s", result.ErrorCode)
		}
		_, _ = fmt.Fprintln(runner.output)
	}
	if exitCode == 0 {
		return nil
	}
	return &ExitError{code: exitCode}
}

func (runner *commandRunner) failureResult(code string) Result {
	result := runner.last
	if result.Version == 0 {
		result.Version = resultVersion
	}
	result.RequestID = runner.requestID
	result.WorkspaceID = runner.workspaceID
	result.ErrorCode = code
	return result
}

func (runner *commandRunner) contextExit(ctx context.Context, fallback int) int {
	if ctx.Err() != nil {
		return 130
	}
	return fallback
}

func (runner *commandRunner) contextErrorCode(ctx context.Context, fallback string) string {
	if ctx.Err() != nil {
		return "interrupted"
	}
	return fallback
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "gateway_unavailable"
	}
	if errors.Is(err, ErrInvalidResponse) {
		return "malformed_response"
	}
	if errors.Is(err, ErrInvalidRequest) {
		return "invalid_request"
	}
	if errors.Is(err, ErrClientUnavailable) {
		return "client_unavailable"
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if code, ok := stableAPIErrorCode(apiErr.Code); ok {
			return code
		}
	}
	for _, code := range []string{
		"repository_not_configured",
		"publication_outcome_unknown",
		"invalid_request",
		"invalid_gate_response",
	} {
		if err.Error() == code ||
			code == "invalid_gate_response" && errors.Is(err, errInvalidGateResponse) {
			return code
		}
	}
	return "gateway_unavailable"
}

func normalizeRepositoryError(err error) error {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "repository_unavailable", "repositories_unavailable", "not_found":
			return errors.New("repository_not_configured")
		}
	}
	return err
}

func stableAPIErrorCode(value string) (string, bool) {
	switch value {
	case "unsafe_provider":
		return "unsafe_provider", true
	case "implement_feature_unavailable", "code_unavailable", "implementation_unavailable":
		return "implementation_unavailable", true
	case "repository_unavailable", "repositories_unavailable":
		return "repository_not_configured", true
	case "invalid_request", "request_id_conflict", "version_conflict":
		return value, true
	default:
		return "", false
	}
}

func aggregateFromAPIError(err error) *prworkspace.Aggregate {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Current
	}
	return nil
}

func validateAggregateBinding(
	aggregate prworkspace.Aggregate,
	workspaceID string,
	repositoryIdentity string,
) error {
	workspace := aggregate.Workspace
	provider := aggregate.ProviderSnapshot
	if !developmentIDPattern.MatchString(workspace.ID) || workspace.Version < 1 ||
		(workspaceID != "" && workspace.ID != workspaceID) ||
		workspace.Intent != prworkspace.IntentImplementFeature ||
		workspace.SourceKind != prworkspace.SourceBrief ||
		provider.Intent != prworkspace.IntentImplementFeature ||
		provider.SourceKind != prworkspace.SourceBrief {
		return ErrInvalidResponse
	}
	if repositoryIdentity == "" {
		return nil
	}
	if workspace.ProviderOrigin == "" || workspace.RepositoryID == "" ||
		workspace.ProviderOrigin != provider.ProviderOrigin ||
		workspace.RepositoryID != provider.RepositoryID ||
		workspace.ProviderOrigin+"|"+workspace.RepositoryID != repositoryIdentity {
		return ErrInvalidResponse
	}
	return nil
}
