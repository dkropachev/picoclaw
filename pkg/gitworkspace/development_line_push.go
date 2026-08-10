package gitworkspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	pinnedLinePushTimeout        = 2 * time.Minute
	pinnedLinePushPostflight     = 30 * time.Second
	maxPinnedLinePushOutputBytes = 64 << 10
)

var (
	// ErrPinnedLinePushOutcomeUnknown reports that a remote update may have
	// happened, but the bounded postflight could not prove the requested tip.
	// Callers must reconcile this external effect rather than automatically
	// issuing another push.
	ErrPinnedLinePushOutcomeUnknown = errors.New(
		"pinned development line push outcome is unknown",
	)
	// ErrPinnedLinePushRemoteUnavailable reports a failed remote observation
	// before any push command was started. Its error text never includes remote
	// stderr or the private repository endpoint.
	ErrPinnedLinePushRemoteUnavailable = errors.New(
		"pinned development line remote is unavailable",
	)
	// ErrPinnedLinePushWorkspaceDrift reports that the requested remote tip was
	// proven while the retained local checkout no longer matched its parked
	// fence. The returned result still describes the proven remote effect.
	ErrPinnedLinePushWorkspaceDrift = errors.New(
		"pinned development line changed during push",
	)
)

// PinnedLinePushRequest authorizes one compare-and-swap update of the source
// branch belonging to an exact parked development-line fence. Repository and
// SourceRef are equality evidence only: the manager derives the actual remote
// and destination from the matching private inventory.
type PinnedLinePushRequest struct {
	Repository            string `json:"-"`
	SourceRef             string `json:"-"`
	ExpectedSourceCommit  string `json:"-"`
	WorkspaceID           string `json:"-"`
	LineID                string `json:"-"`
	ExpectedVersion       int64  `json:"-"`
	ExpectedMutationEpoch int64  `json:"-"`
	ExpectedParkIntentID  string `json:"-"`
	ExpectedBase          string `json:"-"`
	ExpectedTip           string `json:"-"`
	ExpectedTree          string `json:"-"`
	ExpectedRemoteTip     string `json:"-"`
}

// PinnedLinePushDisposition distinguishes a fresh confirmed update from an
// effect-free replay and from a response-loss reconciliation.
type PinnedLinePushDisposition string

const (
	PinnedLinePushApplied        PinnedLinePushDisposition = "applied"
	PinnedLinePushAlreadyCurrent PinnedLinePushDisposition = "already_current"
	PinnedLinePushReconciled     PinnedLinePushDisposition = "reconciled"
)

// PinnedLinePushResult contains only the exact parked and remote ref evidence.
// It deliberately omits the repository URL, checkout path, internal retained
// ref, credentials, and reservation identities.
type PinnedLinePushResult struct {
	WorkspaceID       string                    `json:"-"`
	Version           int64                     `json:"-"`
	MutationEpoch     int64                     `json:"-"`
	ParkIntentID      string                    `json:"-"`
	BaseCommit        string                    `json:"-"`
	Tip               string                    `json:"-"`
	Tree              string                    `json:"-"`
	RemoteRef         string                    `json:"-"`
	ExpectedRemoteTip string                    `json:"-"`
	RemoteTip         string                    `json:"-"`
	Disposition       PinnedLinePushDisposition `json:"-"`
	WorkspaceClean    bool                      `json:"-"`
}

// PushPinnedLine updates exactly the inventory-bound source branch from one
// expected remote commit to the exact parked line tip. It holds the inventory
// lock across the bounded network operation so a Resume or another line
// transition cannot overtake the push. It never writes manager inventory.
func (m *Manager) PushPinnedLine(
	ctx context.Context,
	request PinnedLinePushRequest,
) (PinnedLinePushResult, error) {
	if m == nil {
		return PinnedLinePushResult{}, errors.New(
			"git workspace manager is not configured",
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repository, validationErr := validatePinnedLinePushRequest(
		ctx,
		request,
		m.pinnedLinePushTransport != nil,
	)
	if validationErr != nil {
		return PinnedLinePushResult{}, validationErr
	}
	if operation, ok := ctx.Value(pinnedOperationContextKey{}).(*pinnedOperationToken); ok &&
		operation != nil && operation.active.Load() {
		return PinnedLinePushResult{}, fmt.Errorf(
			"%w: push cannot inherit a mutation operation",
			ErrPinnedLineConflict,
		)
	}

	operationCtx, cancelOperation := context.WithTimeout(ctx, pinnedLinePushTimeout)
	defer cancelOperation()
	m.mu.Lock()
	defer m.mu.Unlock()
	unlockInventory, lockErr := m.lockInventory(operationCtx)
	if lockErr != nil {
		return PinnedLinePushResult{}, lockErr
	}
	defer unlockInventory()
	state, loadErr := m.loadLocked()
	if loadErr != nil {
		return PinnedLinePushResult{}, loadErr
	}
	line := state.DevelopmentLines[request.LineID]
	workspace := state.Workspaces[request.WorkspaceID]
	if matchErr := matchPinnedLinePushFence(
		line,
		workspace,
		request,
		repository,
	); matchErr != nil {
		return PinnedLinePushResult{}, matchErr
	}
	transportRepository, transportErr := m.resolvePinnedLinePushTransport(repository)
	if transportErr != nil {
		return PinnedLinePushResult{}, transportErr
	}

	environment, cleanup, environmentErr := m.newPinnedGitEnvironment()
	if environmentErr != nil {
		return PinnedLinePushResult{}, environmentErr
	}
	defer cleanup()
	environment = pinnedLinePushEnvironment(environment)
	if verifyErr := m.verifyPinnedLinePushLocalState(
		operationCtx,
		workspace,
		line,
		repository,
		request.ExpectedTip,
		request.ExpectedTree,
		environment,
	); verifyErr != nil {
		return PinnedLinePushResult{}, verifyErr
	}
	if ancestryErr := verifyPinnedLinePushAncestry(
		operationCtx,
		workspace.Path,
		line.SourceCommit,
		request.ExpectedRemoteTip,
		request.ExpectedTip,
		environment,
	); ancestryErr != nil {
		return PinnedLinePushResult{}, ancestryErr
	}

	remoteRef := "refs/heads/" + line.SourceRef
	observed, found, observeErr := observePinnedLineRemote(
		operationCtx,
		workspace.Path,
		transportRepository,
		remoteRef,
		len(request.ExpectedTip),
		environment,
	)
	if observeErr != nil {
		if operationErr := operationCtx.Err(); operationErr != nil {
			return PinnedLinePushResult{}, operationErr
		}
		return PinnedLinePushResult{}, ErrPinnedLinePushRemoteUnavailable
	}
	if !found {
		return PinnedLinePushResult{}, fmt.Errorf(
			"%w: source branch is missing from its remote",
			ErrPinnedLineConflict,
		)
	}
	if observed == request.ExpectedTip {
		result := pinnedLinePushResult(
			line,
			request,
			remoteRef,
			PinnedLinePushAlreadyCurrent,
		)
		return m.finishPinnedLinePushLocalPostflight(
			ctx,
			workspace,
			line,
			repository,
			environment,
			result,
		)
	}
	if observed != request.ExpectedRemoteTip {
		return PinnedLinePushResult{}, fmt.Errorf(
			"%w: source branch changed before push",
			ErrPinnedLineConflict,
		)
	}
	if operationErr := operationCtx.Err(); operationErr != nil {
		return PinnedLinePushResult{}, operationErr
	}

	_, pushErr := runPinnedGitPlumbing(
		operationCtx,
		workspace.Path,
		environment,
		nil,
		maxPinnedLinePushOutputBytes,
		"-c",
		"protocol.ext.allow=never",
		"-c",
		"push.negotiate=false",
		"-c",
		"push.useBitmaps=false",
		"push",
		"--quiet",
		"--porcelain",
		"--no-progress",
		"--no-verify",
		"--no-follow-tags",
		"--no-signed",
		"--no-push-option",
		"--recurse-submodules=no",
		"--no-force-if-includes",
		"--no-thin",
		"--force-with-lease="+remoteRef+":"+request.ExpectedRemoteTip,
		"--",
		transportRepository,
		request.ExpectedTip+":"+remoteRef,
	)

	postCtx, cancelPostflight := context.WithTimeout(
		context.WithoutCancel(ctx),
		pinnedLinePushPostflight,
	)
	defer cancelPostflight()
	postObserved, postFound, postObserveErr := observePinnedLineRemote(
		postCtx,
		workspace.Path,
		transportRepository,
		remoteRef,
		len(request.ExpectedTip),
		environment,
	)
	localErr := m.verifyPinnedLinePushLocalState(
		postCtx,
		workspace,
		line,
		repository,
		request.ExpectedTip,
		request.ExpectedTree,
		environment,
	)
	if postObserveErr == nil && postFound && postObserved == request.ExpectedTip {
		disposition := PinnedLinePushApplied
		if pushErr != nil {
			disposition = PinnedLinePushReconciled
		}
		result := pinnedLinePushResult(line, request, remoteRef, disposition)
		if localErr != nil {
			return result, errors.Join(
				ErrPinnedLinePushWorkspaceDrift,
				localErr,
			)
		}
		result.WorkspaceClean = true
		return result, nil
	}

	unknown := []error{ErrPinnedLinePushOutcomeUnknown}
	if pushErr != nil {
		unknown = append(unknown, errors.New(
			"remote push command did not report success",
		))
	}
	if postObserveErr != nil {
		unknown = append(unknown, errors.New("remote push postflight failed"))
	} else if !postFound {
		unknown = append(unknown, errors.New(
			"remote push postflight found no source branch",
		))
	} else {
		unknown = append(unknown, errors.New(
			"remote push postflight did not find the requested tip",
		))
	}
	if localErr != nil {
		unknown = append(
			unknown,
			ErrPinnedLinePushWorkspaceDrift,
			localErr,
		)
	}
	return PinnedLinePushResult{}, errors.Join(unknown...)
}

func validatePinnedLinePushRequest(
	ctx context.Context,
	request PinnedLinePushRequest,
	allowLocalTransport bool,
) (string, error) {
	repositoryInput := strings.TrimSpace(request.Repository)
	if repositoryInput == "" || len(repositoryInput) > 4<<10 ||
		repositoryInput != request.Repository ||
		containsPinnedControlCharacter(repositoryInput) {
		return "", fmt.Errorf(
			"%w: push repository is invalid",
			ErrPinnedLineInvalid,
		)
	}
	repository, repositoryErr := normalizeRepository(repositoryInput)
	if repositoryErr != nil ||
		!validPinnedLinePushRepository(
			repositoryInput,
			repository,
			allowLocalTransport,
		) {
		return "", fmt.Errorf(
			"%w: push repository is not a supported exact transport",
			ErrPinnedLineInvalid,
		)
	}
	if request.SourceRef == "" || len(request.SourceRef) > 4<<10 ||
		request.SourceRef != strings.TrimSpace(request.SourceRef) ||
		!validPinnedSourceRef(ctx, request.SourceRef) ||
		!validPinnedOperationIdentity(request.WorkspaceID, 256) ||
		!validPinnedOperationIdentity(request.LineID, maxDevelopmentLineIdentityBytes) ||
		request.ExpectedVersion < 1 ||
		request.ExpectedVersion > maxDevelopmentLineReservations ||
		request.ExpectedMutationEpoch != request.ExpectedVersion ||
		!validPinnedOperationIdentity(
			request.ExpectedParkIntentID,
			maxDevelopmentLineIdentityBytes,
		) ||
		!validPinnedCommit(request.ExpectedSourceCommit) ||
		!validPinnedCommit(request.ExpectedBase) ||
		!validPinnedCommit(request.ExpectedTip) ||
		!validPinnedCommit(request.ExpectedTree) ||
		!validPinnedCommit(request.ExpectedRemoteTip) ||
		isZeroPinnedLinePushOID(request.ExpectedRemoteTip) ||
		len(request.ExpectedSourceCommit) != len(request.ExpectedTip) ||
		len(request.ExpectedBase) != len(request.ExpectedTip) ||
		len(request.ExpectedTree) != len(request.ExpectedTip) ||
		len(request.ExpectedRemoteTip) != len(request.ExpectedTip) {
		return "", fmt.Errorf(
			"%w: push source or parked fence is invalid",
			ErrPinnedLineInvalid,
		)
	}
	return repository, nil
}

func validPinnedLinePushRepository(
	input, repository string,
	allowLocalTransport bool,
) bool {
	if normalized, ok := normalizeRemoteRepository(repository); ok {
		return normalized == repository && isSCPStyleRemote(repository)
	}
	return allowLocalTransport && input == repository && filepath.IsAbs(input) &&
		filepath.Clean(input) == input &&
		!containsPinnedControlCharacter(repository)
}

func (m *Manager) resolvePinnedLinePushTransport(repository string) (string, error) {
	if m.pinnedLinePushTransport == nil {
		return repository, nil
	}
	target, targetErr := m.pinnedLinePushTransport(repository)
	if targetErr != nil || target == "" || target != strings.TrimSpace(target) ||
		!filepath.IsAbs(target) || filepath.Clean(target) != target ||
		containsPinnedControlCharacter(target) {
		return "", fmt.Errorf(
			"%w: internal push transport is invalid",
			ErrPinnedLineInvalid,
		)
	}
	return target, nil
}

func isZeroPinnedLinePushOID(value string) bool {
	return value == strings.Repeat("0", len(value))
}

func matchPinnedLinePushFence(
	line *developmentLineRecord,
	workspace *WorkspaceRecord,
	request PinnedLinePushRequest,
	repository string,
) error {
	if line == nil || workspace == nil || line.State != developmentLineParked ||
		line.PendingParkSet || line.WorkspaceID != request.WorkspaceID ||
		line.RepoID != repoID(repository) || line.SourceRef != request.SourceRef ||
		line.SourceCommit != request.ExpectedSourceCommit ||
		line.Version != request.ExpectedVersion ||
		line.MutationEpoch != request.ExpectedMutationEpoch ||
		line.LastParkIntentID != request.ExpectedParkIntentID ||
		line.LastParkEpoch != request.ExpectedMutationEpoch ||
		line.LastParkPreviousTip != request.ExpectedBase ||
		line.Tip != request.ExpectedTip || line.Tree != request.ExpectedTree ||
		workspace.ID != request.WorkspaceID || workspace.RepoID != line.RepoID ||
		workspace.RemoteURL != repository || workspace.Ref != request.SourceRef ||
		workspace.PinnedSourceRef != request.SourceRef ||
		workspace.PinnedCommit != request.ExpectedSourceCommit ||
		workspace.DevelopmentLineID != request.LineID || workspace.LockedBy != nil {
		return fmt.Errorf(
			"%w: pinned development line push fence changed",
			ErrPinnedLineConflict,
		)
	}
	return nil
}

func pinnedLinePushEnvironment(environment []string) []string {
	// The operator's HOME and SSH_AUTH_SOCK deliberately remain available for
	// host-key and credential selection. Repository-local transport overrides,
	// ambient Git variables, prompts, and askpass are rejected or replaced.
	result := append([]string(nil), environment...)
	return append(
		result,
		"GIT_SSH_COMMAND=ssh -oBatchMode=yes -oClearAllForwardings=yes "+
			"-oPermitLocalCommand=no",
		"GIT_SSH_VARIANT=ssh",
		"SSH_ASKPASS_REQUIRE=never",
	)
}

func verifyPinnedLinePushConfiguration(
	ctx context.Context,
	directory string,
	environment []string,
) error {
	configuration, configErr := runPinnedGit(
		ctx,
		directory,
		environment,
		"config",
		"--local",
		"--no-includes",
		"--null",
		"--list",
	)
	if configErr != nil {
		return fmt.Errorf(
			"%w: inspect pinned push configuration: %v",
			ErrPinnedLineConflict,
			configErr,
		)
	}
	for _, entry := range strings.Split(configuration, "\x00") {
		if entry == "" {
			continue
		}
		key, _, _ := strings.Cut(entry, "\n")
		key = strings.ToLower(strings.TrimSpace(key))
		if unsafePinnedLinePushConfigKey(key) {
			return fmt.Errorf(
				"%w: pinned workspace uses unsafe push configuration %s",
				ErrPinnedLineConflict,
				key,
			)
		}
	}
	return nil
}

func (m *Manager) verifyPinnedLinePushLocalState(
	ctx context.Context,
	workspace *WorkspaceRecord,
	line *developmentLineRecord,
	repository, tip, tree string,
	environment []string,
) error {
	if verifyErr := m.verifyDevelopmentLineParkedWorkspace(
		ctx,
		workspace,
		line,
		repository,
		tip,
		tree,
		environment,
	); verifyErr != nil {
		return verifyErr
	}
	return verifyPinnedLinePushConfiguration(ctx, workspace.Path, environment)
}

func unsafePinnedLinePushConfigKey(key string) bool {
	switch key {
	case "core.gitproxy", "ssh.variant", "remote.pushdefault":
		return true
	}
	if strings.HasPrefix(key, "push.") || strings.HasPrefix(key, "http.") {
		return true
	}
	if strings.HasPrefix(key, "branch.") && strings.HasSuffix(key, ".pushremote") {
		return true
	}
	if !strings.HasPrefix(key, "remote.") {
		return false
	}
	for _, suffix := range []string{
		".mirror",
		".proxy",
		".proxyauthmethod",
		".push",
		".pushurl",
		".receivepack",
		".serveroption",
		".uploadpack",
		".vcs",
	} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}

func verifyPinnedLinePushAncestry(
	ctx context.Context,
	directory, source, remote, tip string,
	environment []string,
) error {
	resolved, resolveErr := resolvePinnedGitCommit(ctx, directory, remote, environment)
	if resolveErr != nil || resolved != remote {
		return fmt.Errorf(
			"%w: expected remote tip is not an exact local commit",
			ErrPinnedLineConflict,
		)
	}
	for _, pair := range [][2]string{{source, remote}, {remote, tip}} {
		if _, ancestryErr := runPinnedGitPlumbing(
			ctx,
			directory,
			environment,
			nil,
			maxPinnedLinePushOutputBytes,
			"merge-base",
			"--is-ancestor",
			pair[0],
			pair[1],
		); ancestryErr != nil {
			return fmt.Errorf(
				"%w: remote update is not forward-only on the retained line",
				ErrPinnedLineConflict,
			)
		}
	}
	return nil
}

func observePinnedLineRemote(
	ctx context.Context,
	directory, repository, remoteRef string,
	oidWidth int,
	environment []string,
) (string, bool, error) {
	output, observeErr := runPinnedGitPlumbing(
		ctx,
		directory,
		environment,
		nil,
		maxPinnedLinePushOutputBytes,
		"-c",
		"protocol.ext.allow=never",
		"ls-remote",
		"--quiet",
		"--refs",
		"--",
		repository,
		remoteRef,
	)
	if observeErr != nil {
		return "", false, observeErr
	}
	if len(output) == 0 {
		return "", false, nil
	}
	if bytes.IndexAny(output, "\x00\r") >= 0 || output[len(output)-1] != '\n' {
		return "", false, errors.New("remote ref advertisement is malformed")
	}
	line := output[:len(output)-1]
	if bytes.IndexByte(line, '\n') >= 0 {
		return "", false, errors.New("remote ref advertisement is not unique")
	}
	oid, advertisedRef, found := bytes.Cut(line, []byte{'\t'})
	if !found || len(oid) != oidWidth || !validPinnedCommit(string(oid)) ||
		string(advertisedRef) != remoteRef {
		return "", false, errors.New("remote ref advertisement is invalid")
	}
	return string(oid), true, nil
}

func pinnedLinePushResult(
	line *developmentLineRecord,
	request PinnedLinePushRequest,
	remoteRef string,
	disposition PinnedLinePushDisposition,
) PinnedLinePushResult {
	return PinnedLinePushResult{
		WorkspaceID:       request.WorkspaceID,
		Version:           line.Version,
		MutationEpoch:     line.MutationEpoch,
		ParkIntentID:      line.LastParkIntentID,
		BaseCommit:        line.LastParkPreviousTip,
		Tip:               line.Tip,
		Tree:              line.Tree,
		RemoteRef:         remoteRef,
		ExpectedRemoteTip: request.ExpectedRemoteTip,
		RemoteTip:         line.Tip,
		Disposition:       disposition,
	}
}

func (m *Manager) finishPinnedLinePushLocalPostflight(
	ctx context.Context,
	workspace *WorkspaceRecord,
	line *developmentLineRecord,
	repository string,
	environment []string,
	result PinnedLinePushResult,
) (PinnedLinePushResult, error) {
	postCtx, cancelPostflight := context.WithTimeout(
		context.WithoutCancel(ctx),
		pinnedLinePushPostflight,
	)
	defer cancelPostflight()
	if localErr := m.verifyPinnedLinePushLocalState(
		postCtx,
		workspace,
		line,
		repository,
		result.Tip,
		result.Tree,
		environment,
	); localErr != nil {
		return result, errors.Join(
			ErrPinnedLinePushWorkspaceDrift,
			localErr,
		)
	}
	result.WorkspaceClean = true
	return result, nil
}
