package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	// PRWorkspaceCheckpointBrokerDomain is the typed checkpoint RPC domain.
	PRWorkspaceCheckpointBrokerDomain  = "pr-workspace-checkpoints"
	prWorkspaceCheckpointBrokerVersion = 1

	// PRWorkspaceCheckpointStoreID is the fixed opaque catalog identity. The
	// physical location is derived only inside the trusted broker process.
	PRWorkspaceCheckpointStoreID database.StoreID = "global.pr-workspace-checkpoints"

	prWorkspaceCheckpointOperationSave               = "save"
	prWorkspaceCheckpointOperationLoad               = "load"
	prWorkspaceCheckpointOperationRemove             = "remove"
	prWorkspaceCheckpointOperationRemovalMatches     = "removal-matches"
	prWorkspaceCheckpointOperationReconcileFinalized = "reconcile-finalized"
)

type prWorkspaceCheckpointRevisionWire struct {
	WorkspaceID string `json:"workspace_id"`
	Sequence    int64  `json:"sequence"`
	StateDigest string `json:"state_digest,omitempty"`
	Exists      bool   `json:"exists"`
}

type prWorkspaceCheckpointSaveRequest struct {
	StoreID    database.StoreID                  `json:"store_id"`
	Checkpoint prWorkspaceCandidateCheckpoint    `json:"checkpoint"`
	Expected   prWorkspaceCheckpointRevisionWire `json:"expected"`
}

type prWorkspaceCheckpointLookupRequest struct {
	StoreID     database.StoreID                  `json:"store_id"`
	WorkspaceID string                            `json:"workspace_id"`
	Expected    prWorkspaceCheckpointRevisionWire `json:"expected,omitempty"`
}

type prWorkspaceCheckpointReconcileRequest struct {
	StoreID    database.StoreID                  `json:"store_id"`
	Checkpoint prWorkspaceCandidateCheckpoint    `json:"checkpoint"`
	Expected   prWorkspaceCheckpointRevisionWire `json:"expected"`
}

type prWorkspaceCheckpointBrokerResponse struct {
	Checkpoint prWorkspaceCandidateCheckpoint    `json:"checkpoint"`
	Revision   prWorkspaceCheckpointRevisionWire `json:"revision"`
	Found      bool                              `json:"found"`
	Matched    bool                              `json:"matched"`
	Completed  bool                              `json:"completed"`
}

// PRWorkspaceCheckpointBrokerHandler owns the one retained checkpoint pool.
// It accepts only the fixed logical StoreID and never accepts a physical path.
type PRWorkspaceCheckpointBrokerHandler struct {
	store *prWorkspaceCandidateCheckpointStore

	mu        sync.RWMutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

// NewPRWorkspaceCheckpointBrokerHandler derives the provider root from the
// broker-loaded configuration and retains its pool until broker shutdown.
func NewPRWorkspaceCheckpointBrokerHandler(
	home string,
	cfg *config.Config,
) (*PRWorkspaceCheckpointBrokerHandler, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"PR workspace checkpoint handler requires authenticated broker authority",
		)
	}
	root, err := trustedPRWorkspaceCheckpointRoot(home, cfg)
	if err != nil {
		return nil, mapPRWorkspaceCheckpointBrokerError(err)
	}
	store, err := newLocalPRWorkspaceCandidateCheckpointStore(root, true)
	if err != nil {
		return nil, mapPRWorkspaceCheckpointBrokerError(err)
	}
	return &PRWorkspaceCheckpointBrokerHandler{store: store}, nil
}

func (handler *PRWorkspaceCheckpointBrokerHandler) Handle(
	ctx context.Context,
	request database.Request,
) (any, error) {
	if handler == nil || request.Domain != PRWorkspaceCheckpointBrokerDomain ||
		request.Version != prWorkspaceCheckpointBrokerVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, database.NewError(database.CodeDeadline, "PR workspace checkpoint deadline was exceeded")
	}
	handler.mu.RLock()
	defer handler.mu.RUnlock()
	if handler.closed || handler.store == nil {
		return nil, database.NewError(database.CodeUnavailable, "PR workspace checkpoint broker is closed")
	}

	switch request.Operation {
	case prWorkspaceCheckpointOperationSave:
		var input prWorkspaceCheckpointSaveRequest
		if request.DecodePayload(&input) != nil || input.StoreID != PRWorkspaceCheckpointStoreID ||
			!validPRWorkspaceCandidateCheckpointShape(input.Checkpoint) {
			return nil, invalidPRWorkspaceCheckpointBrokerRequest()
		}
		expected, err := decodePRWorkspaceCheckpointRevision(input.Expected)
		if err != nil || !validPRWorkspaceCheckpointRevision(expected, input.Checkpoint.WorkspaceID) {
			return nil, invalidPRWorkspaceCheckpointBrokerRequest()
		}
		revision, err := handler.store.save(ctx, input.Checkpoint, expected)
		if err != nil {
			return nil, mapPRWorkspaceCheckpointBrokerError(err)
		}
		return prWorkspaceCheckpointBrokerResponse{
			Revision: encodePRWorkspaceCheckpointRevision(revision), Completed: true,
		}, nil

	case prWorkspaceCheckpointOperationLoad:
		input, err := decodePRWorkspaceCheckpointLookupRequest(request, false)
		if err != nil {
			return nil, err
		}
		checkpoint, revision, found, err := handler.store.load(ctx, input.WorkspaceID)
		if err != nil {
			return nil, mapPRWorkspaceCheckpointBrokerError(err)
		}
		return prWorkspaceCheckpointBrokerResponse{
			Checkpoint: checkpoint, Revision: encodePRWorkspaceCheckpointRevision(revision), Found: found,
		}, nil

	case prWorkspaceCheckpointOperationRemove:
		input, err := decodePRWorkspaceCheckpointLookupRequest(request, true)
		if err != nil {
			return nil, err
		}
		expected, _ := decodePRWorkspaceCheckpointRevision(input.Expected)
		if err = handler.store.remove(ctx, input.WorkspaceID, expected); err != nil {
			return nil, mapPRWorkspaceCheckpointBrokerError(err)
		}
		return prWorkspaceCheckpointBrokerResponse{Completed: true}, nil

	case prWorkspaceCheckpointOperationRemovalMatches:
		input, err := decodePRWorkspaceCheckpointLookupRequest(request, true)
		if err != nil {
			return nil, err
		}
		expected, _ := decodePRWorkspaceCheckpointRevision(input.Expected)
		matched, err := handler.store.removalMatchesContext(ctx, input.WorkspaceID, expected)
		if err != nil {
			return nil, mapPRWorkspaceCheckpointBrokerError(err)
		}
		return prWorkspaceCheckpointBrokerResponse{Matched: matched}, nil

	case prWorkspaceCheckpointOperationReconcileFinalized:
		var input prWorkspaceCheckpointReconcileRequest
		if request.DecodePayload(&input) != nil || input.StoreID != PRWorkspaceCheckpointStoreID ||
			!validPRWorkspaceCandidateCheckpointShape(input.Checkpoint) ||
			input.Checkpoint.State != prWorkspaceCandidateCheckpointParked || input.Checkpoint.Fence == nil {
			return nil, invalidPRWorkspaceCheckpointBrokerRequest()
		}
		expected, err := decodePRWorkspaceCheckpointRevision(input.Expected)
		if err != nil || !expected.exists ||
			!validPRWorkspaceCheckpointRevision(expected, input.Checkpoint.WorkspaceID) {
			return nil, invalidPRWorkspaceCheckpointBrokerRequest()
		}
		revision, matched, err := handler.store.reconcileFinalizedContext(ctx, input.Checkpoint, expected)
		if err != nil {
			return nil, mapPRWorkspaceCheckpointBrokerError(err)
		}
		response := prWorkspaceCheckpointBrokerResponse{Matched: matched}
		if matched {
			response.Revision = encodePRWorkspaceCheckpointRevision(revision)
		}
		return response, nil

	default:
		return nil, database.NewError(
			database.CodeUnsupported,
			"PR workspace checkpoint operation is unsupported",
		)
	}
}

func (handler *PRWorkspaceCheckpointBrokerHandler) Close() error {
	if handler == nil {
		return nil
	}
	handler.closeOnce.Do(func() {
		handler.mu.Lock()
		defer handler.mu.Unlock()
		handler.closed = true
		if handler.store != nil {
			handler.closeErr = handler.store.close()
		}
	})
	return handler.closeErr
}

// RunOfflinePRWorkspaceCheckpointMigration initializes, upgrades, imports,
// and validates the trusted checkpoint root under the exclusive home fence.
func RunOfflinePRWorkspaceCheckpointMigration(ctx context.Context, root string) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(
			database.CodeConflict,
			"PR workspace checkpoint migration requires the exclusive database fence",
		)
	}
	store, err := newLocalPRWorkspaceCandidateCheckpointStore(root, true)
	if err != nil {
		return err
	}
	return store.close()
}

func trustedPRWorkspaceCheckpointRoot(home string, cfg *config.Config) (string, error) {
	canonicalHome, err := database.CanonicalHome(home)
	if err != nil {
		return "", err
	}
	trusted := config.Config{}
	if cfg != nil {
		trusted = *cfg
	}
	if strings.TrimSpace(trusted.Agents.Defaults.Workspace) == "" {
		trusted.Agents.Defaults.Workspace = filepath.Join(canonicalHome, "workspace")
	}
	gitRoot := strings.TrimSpace(trusted.GitWorkspaceRootPath())
	if gitRoot == "" || strings.ContainsRune(gitRoot, 0) {
		return "", database.NewError(database.CodeInvalid, "PR workspace checkpoint configuration is invalid")
	}
	if !filepath.IsAbs(gitRoot) {
		gitRoot = filepath.Join(canonicalHome, gitRoot)
	}
	gitRoot, err = filepath.Abs(filepath.Clean(gitRoot))
	if err != nil {
		return "", database.NewError(database.CodeInvalid, "PR workspace checkpoint configuration is invalid")
	}
	return filepath.Join(gitRoot, ".pr-workspace-implementation", "active"), nil
}

func decodePRWorkspaceCheckpointLookupRequest(
	request database.Request,
	requireExpected bool,
) (prWorkspaceCheckpointLookupRequest, error) {
	var input prWorkspaceCheckpointLookupRequest
	if request.DecodePayload(&input) != nil || input.StoreID != PRWorkspaceCheckpointStoreID ||
		stringsTrimmed(input.WorkspaceID) == "" {
		return prWorkspaceCheckpointLookupRequest{}, invalidPRWorkspaceCheckpointBrokerRequest()
	}
	if !requireExpected {
		if input.Expected != (prWorkspaceCheckpointRevisionWire{}) {
			return prWorkspaceCheckpointLookupRequest{}, invalidPRWorkspaceCheckpointBrokerRequest()
		}
		return input, nil
	}
	expected, err := decodePRWorkspaceCheckpointRevision(input.Expected)
	if err != nil || !validPRWorkspaceCheckpointRevision(expected, input.WorkspaceID) {
		return prWorkspaceCheckpointLookupRequest{}, invalidPRWorkspaceCheckpointBrokerRequest()
	}
	return input, nil
}

func (store *prWorkspaceCandidateCheckpointStore) saveBroker(
	ctx context.Context,
	checkpoint prWorkspaceCandidateCheckpoint,
	expected prWorkspaceCandidateCheckpointRevision,
) (prWorkspaceCandidateCheckpointRevision, error) {
	var response prWorkspaceCheckpointBrokerResponse
	err := store.callCheckpointBroker(ctx, prWorkspaceCheckpointOperationSave, prWorkspaceCheckpointSaveRequest{
		StoreID: store.storeID, Checkpoint: checkpoint,
		Expected: encodePRWorkspaceCheckpointRevision(expected),
	}, &response, true)
	if err != nil {
		return prWorkspaceCandidateCheckpointRevision{}, checkpointClientError(err)
	}
	revision, err := decodePRWorkspaceCheckpointRevision(response.Revision)
	if err != nil || !response.Completed || !revision.exists ||
		!validPRWorkspaceCheckpointRevision(revision, checkpoint.WorkspaceID) {
		return prWorkspaceCandidateCheckpointRevision{}, database.NewError(
			database.CodeIntegrity,
			"PR workspace checkpoint broker response is invalid",
		)
	}
	return revision, nil
}

func (store *prWorkspaceCandidateCheckpointStore) loadBroker(
	ctx context.Context,
	workspaceID string,
) (prWorkspaceCandidateCheckpoint, prWorkspaceCandidateCheckpointRevision, bool, error) {
	var response prWorkspaceCheckpointBrokerResponse
	err := store.callCheckpointBroker(ctx, prWorkspaceCheckpointOperationLoad, prWorkspaceCheckpointLookupRequest{
		StoreID: store.storeID, WorkspaceID: workspaceID,
	}, &response, false)
	if err != nil {
		return prWorkspaceCandidateCheckpoint{}, prWorkspaceCandidateCheckpointRevision{}, false, err
	}
	revision, err := decodePRWorkspaceCheckpointRevision(response.Revision)
	if err != nil || !validPRWorkspaceCheckpointRevision(revision, workspaceID) ||
		(response.Found != revision.exists) ||
		(response.Found && (!validPRWorkspaceCandidateCheckpointShape(response.Checkpoint) ||
			response.Checkpoint.WorkspaceID != workspaceID)) ||
		(!response.Found && response.Checkpoint != (prWorkspaceCandidateCheckpoint{})) {
		return prWorkspaceCandidateCheckpoint{}, prWorkspaceCandidateCheckpointRevision{}, false,
			database.NewError(database.CodeIntegrity, "PR workspace checkpoint broker response is invalid")
	}
	return response.Checkpoint, revision, response.Found, nil
}

func (store *prWorkspaceCandidateCheckpointStore) removeBroker(
	ctx context.Context,
	workspaceID string,
	expected prWorkspaceCandidateCheckpointRevision,
) error {
	var response prWorkspaceCheckpointBrokerResponse
	err := store.callCheckpointBroker(ctx, prWorkspaceCheckpointOperationRemove, prWorkspaceCheckpointLookupRequest{
		StoreID: store.storeID, WorkspaceID: workspaceID,
		Expected: encodePRWorkspaceCheckpointRevision(expected),
	}, &response, true)
	if err != nil {
		return checkpointClientError(err)
	}
	if !response.Completed {
		return database.NewError(database.CodeIntegrity, "PR workspace checkpoint broker response is invalid")
	}
	return nil
}

func (store *prWorkspaceCandidateCheckpointStore) removalMatchesBroker(
	ctx context.Context,
	workspaceID string,
	expected prWorkspaceCandidateCheckpointRevision,
) (bool, error) {
	var response prWorkspaceCheckpointBrokerResponse
	err := store.callCheckpointBroker(
		ctx,
		prWorkspaceCheckpointOperationRemovalMatches,
		prWorkspaceCheckpointLookupRequest{
			StoreID: store.storeID, WorkspaceID: workspaceID,
			Expected: encodePRWorkspaceCheckpointRevision(expected),
		},
		&response,
		false,
	)
	return response.Matched, err
}

func (store *prWorkspaceCandidateCheckpointStore) reconcileFinalizedBroker(
	ctx context.Context,
	checkpoint prWorkspaceCandidateCheckpoint,
	expected prWorkspaceCandidateCheckpointRevision,
) (prWorkspaceCandidateCheckpointRevision, bool, error) {
	var response prWorkspaceCheckpointBrokerResponse
	err := store.callCheckpointBroker(
		ctx,
		prWorkspaceCheckpointOperationReconcileFinalized,
		prWorkspaceCheckpointReconcileRequest{
			StoreID: store.storeID, Checkpoint: checkpoint,
			Expected: encodePRWorkspaceCheckpointRevision(expected),
		},
		&response,
		false,
	)
	if err != nil || !response.Matched {
		return prWorkspaceCandidateCheckpointRevision{}, false, err
	}
	revision, err := decodePRWorkspaceCheckpointRevision(response.Revision)
	if err != nil || !revision.exists ||
		!validPRWorkspaceCheckpointRevision(revision, checkpoint.WorkspaceID) {
		return prWorkspaceCandidateCheckpointRevision{}, false, database.NewError(
			database.CodeIntegrity,
			"PR workspace checkpoint broker response is invalid",
		)
	}
	return revision, true, nil
}

func (store *prWorkspaceCandidateCheckpointStore) callCheckpointBroker(
	ctx context.Context,
	operation string,
	input any,
	output any,
	mutation bool,
) error {
	if store == nil || store.broker == nil || store.storeID != PRWorkspaceCheckpointStoreID {
		return database.NewError(database.CodeUnavailable, "PR workspace checkpoint broker is unavailable")
	}
	if mutation {
		return store.broker.CallWithOptions(
			ctx,
			PRWorkspaceCheckpointBrokerDomain,
			prWorkspaceCheckpointBrokerVersion,
			operation,
			input,
			output,
			database.CallOptions{Mutation: true},
		)
	}
	return store.broker.Call(
		ctx,
		PRWorkspaceCheckpointBrokerDomain,
		prWorkspaceCheckpointBrokerVersion,
		operation,
		input,
		output,
	)
}

func encodePRWorkspaceCheckpointRevision(
	revision prWorkspaceCandidateCheckpointRevision,
) prWorkspaceCheckpointRevisionWire {
	encoded := prWorkspaceCheckpointRevisionWire{
		WorkspaceID: revision.workspaceID,
		Sequence:    revision.sequence,
		Exists:      revision.exists,
	}
	if revision.exists {
		encoded.StateDigest = hex.EncodeToString(revision.stateDigest[:])
	}
	return encoded
}

func decodePRWorkspaceCheckpointRevision(
	encoded prWorkspaceCheckpointRevisionWire,
) (prWorkspaceCandidateCheckpointRevision, error) {
	revision := prWorkspaceCandidateCheckpointRevision{
		workspaceID: encoded.WorkspaceID,
		sequence:    encoded.Sequence,
		exists:      encoded.Exists,
	}
	if !encoded.Exists {
		if encoded.StateDigest != "" {
			return prWorkspaceCandidateCheckpointRevision{}, errors.New("checkpoint revision is invalid")
		}
		return revision, nil
	}
	if len(encoded.StateDigest) != sha256.Size*2 || strings.ToLower(encoded.StateDigest) != encoded.StateDigest {
		return prWorkspaceCandidateCheckpointRevision{}, errors.New("checkpoint revision is invalid")
	}
	digest, err := hex.DecodeString(encoded.StateDigest)
	if err != nil || len(digest) != sha256.Size {
		return prWorkspaceCandidateCheckpointRevision{}, errors.New("checkpoint revision is invalid")
	}
	copy(revision.stateDigest[:], digest)
	return revision, nil
}

func checkpointClientError(err error) error {
	if database.CodeOf(err) == database.CodeConflict {
		return errors.Join(errPRWorkspaceCandidateCheckpointConflict, err)
	}
	return err
}

func invalidPRWorkspaceCheckpointBrokerRequest() error {
	return database.NewError(database.CodeInvalid, "PR workspace checkpoint request is invalid")
}

func mapPRWorkspaceCheckpointBrokerError(err error) error {
	if err == nil {
		return nil
	}
	if code := database.CodeOf(err); code != database.CodeInternal {
		return database.NewError(code, "PR workspace checkpoint operation failed")
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return database.NewError(database.CodeDeadline, "PR workspace checkpoint deadline was exceeded")
	case errors.Is(err, errPRWorkspaceCandidateCheckpointConflict),
		errors.Is(err, errPRWorkspaceCheckpointCapacity):
		return database.NewError(database.CodeConflict, "PR workspace checkpoint changed concurrently")
	case errors.Is(err, sqlitestore.ErrTooNew):
		return database.NewError(database.CodeUnsupported, "PR workspace checkpoint schema is newer than supported")
	case errors.Is(err, sqlitestore.ErrInvalidSchema), errors.Is(err, sqlitestore.ErrIntegrity):
		return database.NewError(database.CodeIntegrity, "PR workspace checkpoint integrity validation failed")
	case errors.Is(err, os.ErrPermission):
		return database.NewError(database.CodeUnavailable, "PR workspace checkpoint store is unavailable")
	default:
		return database.NewError(database.CodeInternal, "PR workspace checkpoint operation failed")
	}
}

var _ database.Handler = (*PRWorkspaceCheckpointBrokerHandler)(nil)
