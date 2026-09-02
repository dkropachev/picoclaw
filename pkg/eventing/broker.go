//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	dbcatalog "github.com/sipeed/picoclaw/pkg/database/catalog"
	developmentnotifications "github.com/sipeed/picoclaw/pkg/developmentnotifications"
)

const (
	BrokerDomain                     = "eventing"
	BrokerVersion                    = 1
	EventingStoreID database.StoreID = "workspace.eventing"

	eventingOpInsert                        = "insert"
	eventingOpGet                           = "get"
	eventingOpList                          = "list"
	eventingOpGetEventMetadata              = "get-event-metadata"
	eventingOpGetEventPayload               = "get-event-payload"
	eventingOpListEventMetadata             = "list-event-metadata"
	eventingOpClaimRouting                  = "claim-routing"
	eventingOpRenewRouting                  = "renew-routing"
	eventingOpAckRouting                    = "ack-routing"
	eventingOpNackRouting                   = "nack-routing"
	eventingOpDeadRouting                   = "dead-routing"
	eventingOpCreateDispatchClaim           = "create-dispatch-for-claim"
	eventingOpCreateRevisionedDispatchClaim = "create-revisioned-dispatch-for-claim"
	eventingOpCreateDispatch                = "create-dispatch"
	eventingOpGetDispatch                   = "get-dispatch"
	eventingOpGetDispatchMetadata           = "get-dispatch-metadata"
	eventingOpClaimDispatches               = "claim-dispatches"
	eventingOpLinkDispatchRun               = "link-dispatch-run"
	eventingOpRenewDispatch                 = "renew-dispatch"
	eventingOpFinishDispatch                = "finish-dispatch"
	eventingOpNackDispatch                  = "nack-dispatch"
	eventingOpListDispatches                = "list-dispatches"
	eventingOpListDispatchMetadata          = "list-dispatch-metadata"
	eventingOpReplay                        = "replay"
	eventingOpPrune                         = "prune"
	eventingOpSetPRCutover                  = "set-pr-cutover"
	eventingOpGetPRCutover                  = "get-pr-cutover"
	eventingOpCreatePRWorkspace             = "create-pr-workspace"
	eventingOpGetPRWorkspace                = "get-pr-workspace"
	eventingOpListPRWorkspaces              = "list-pr-workspaces"
	eventingOpApplyPRMutation               = "apply-pr-mutation"
	eventingOpApplyPRPatch                  = "apply-pr-patch"
	eventingOpClaimPROperations             = "claim-pr-operations"
	eventingOpFinishPROperation             = "finish-pr-operation"
	eventingOpClaimPRPublications           = "claim-pr-publications"
	eventingOpFinishPRPublication           = "finish-pr-publication"
	eventingOpUpsertNotification            = "upsert-notification"
	eventingOpListNotifications             = "list-notifications"
	eventingOpListPushNotifications         = "list-push-notifications"
	eventingOpGetNotification               = "get-notification"
	eventingOpMutateNotification            = "mutate-notification"
	eventingOpMutateNotifications           = "mutate-notifications"
	eventingOpGetNotificationViews          = "get-notification-views"
	eventingOpPutNotificationViews          = "put-notification-views"
	eventingOpGetPushState                  = "get-push-state"
	eventingOpPutPushState                  = "put-push-state"
	eventingOpPruneNotifications            = "prune-notifications"
	eventingOpResolveStore                  = "resolve-store"
	// BrokerPreflightOperation opens and fully validates exactly one cataloged
	// event store without mutating domain data.
	BrokerPreflightOperation = "preflight"

	eventingNotificationPageSize = 200
)

type eventingResolveRequest struct {
	WorkspaceSelector string `json:"workspace_selector"`
}

type eventingResolveResponse struct {
	StoreID database.StoreID `json:"store_id"`
}

type eventingBrokerTarget struct {
	StoreID database.StoreID `json:"store_id"`
}

type eventingBrokerRequest struct {
	StoreID             database.StoreID                     `json:"store_id"`
	Envelope            Envelope                             `json:"envelope,omitempty"`
	ID                  string                               `json:"id,omitempty"`
	Source              string                               `json:"source,omitempty"`
	Connector           string                               `json:"connector,omitempty"`
	EventFilter         EventFilter                          `json:"event_filter,omitempty"`
	DispatchFilter      DispatchFilter                       `json:"dispatch_filter,omitempty"`
	WorkerLabel         string                               `json:"worker_label,omitempty"`
	Limit               int                                  `json:"limit,omitempty"`
	Offset              int                                  `json:"offset,omitempty"`
	Lease               time.Duration                        `json:"lease,omitempty"`
	LeaseToken          string                               `json:"lease_token,omitempty"`
	AvailableAt         time.Time                            `json:"available_at,omitempty"`
	Detail              string                               `json:"detail,omitempty"`
	WorkflowRef         string                               `json:"workflow_ref,omitempty"`
	WorkflowRevision    string                               `json:"workflow_revision,omitempty"`
	RunID               string                               `json:"run_id,omitempty"`
	DispatchStatus      DispatchStatus                       `json:"dispatch_status,omitempty"`
	Before              time.Time                            `json:"before,omitempty"`
	Watermark           PRIngressCutoverWatermark            `json:"watermark,omitempty"`
	PRCreate            PRWorkspaceCreate                    `json:"pr_create,omitempty"`
	WorkspaceID         string                               `json:"workspace_id,omitempty"`
	PRFilter            PRWorkspaceFilter                    `json:"pr_filter,omitempty"`
	PRMutation          PRWorkspaceMutation                  `json:"pr_mutation,omitempty"`
	PRPatch             PRWorkspacePatchMutation             `json:"pr_patch,omitempty"`
	PRClaim             PRWorkspaceClaimRequest              `json:"pr_claim,omitempty"`
	PROperationFinish   PRWorkspaceOperationFinish           `json:"pr_operation_finish,omitempty"`
	PRPublicationFinish PRWorkspacePublicationFinish         `json:"pr_publication_finish,omitempty"`
	NotificationDraft   developmentnotifications.Draft       `json:"notification_draft,omitempty"`
	ExpectedVersion     uint64                               `json:"expected_version,omitempty"`
	Action              string                               `json:"action,omitempty"`
	SnoozedUntil        *time.Time                           `json:"snoozed_until,omitempty"`
	NotificationBulk    DevelopmentNotificationBulkMutation  `json:"notification_bulk,omitempty"`
	Views               []developmentnotifications.SavedView `json:"views,omitempty"`
	PushState           json.RawMessage                      `json:"push_state,omitempty"`
}

type eventingBrokerResponse struct {
	Ready                bool                                      `json:"ready,omitempty"`
	Mutation             bool                                      `json:"mutation,omitempty"`
	Insert               InsertResult                              `json:"insert,omitempty"`
	Event                StoredEvent                               `json:"event,omitempty"`
	EventPage            EventPage                                 `json:"event_page,omitempty"`
	EventMetadata        StoredEventMetadata                       `json:"event_metadata,omitempty"`
	EventPayload         []byte                                    `json:"event_payload,omitempty"`
	EventMetadataPage    EventMetadataPage                         `json:"event_metadata_page,omitempty"`
	Events               []StoredEvent                             `json:"events,omitempty"`
	Dispatch             Dispatch                                  `json:"dispatch,omitempty"`
	DispatchCreated      bool                                      `json:"dispatch_created,omitempty"`
	DispatchMetadata     DispatchMetadata                          `json:"dispatch_metadata,omitempty"`
	Dispatches           []Dispatch                                `json:"dispatches,omitempty"`
	DispatchPage         DispatchPage                              `json:"dispatch_page,omitempty"`
	DispatchMetadataPage DispatchMetadataPage                      `json:"dispatch_metadata_page,omitempty"`
	Count                int64                                     `json:"count,omitempty"`
	Watermark            PRIngressCutoverWatermark                 `json:"watermark,omitempty"`
	PRAggregate          PRWorkspaceAggregate                      `json:"pr_aggregate,omitempty"`
	PRCreated            bool                                      `json:"pr_created,omitempty"`
	PRPage               PRWorkspacePage                           `json:"pr_page,omitempty"`
	PRMutationResult     PRWorkspaceMutationResult                 `json:"pr_mutation_result,omitempty"`
	PRPatchResult        PRWorkspacePatchResult                    `json:"pr_patch_result,omitempty"`
	PROperations         []PRClaimedOperationIntent                `json:"pr_operations,omitempty"`
	PROperation          PRClaimedOperationIntent                  `json:"pr_operation,omitempty"`
	PRPublications       []PRClaimedPublication                    `json:"pr_publications,omitempty"`
	PRPublication        PRClaimedPublication                      `json:"pr_publication,omitempty"`
	NotificationUpsert   developmentnotifications.UpsertResult     `json:"notification_upsert,omitempty"`
	Notifications        []developmentnotifications.Notification   `json:"notifications,omitempty"`
	Notification         developmentnotifications.Notification     `json:"notification,omitempty"`
	NotificationBulk     DevelopmentNotificationBulkMutationResult `json:"notification_bulk,omitempty"`
	NotificationViews    DevelopmentNotificationViewsDocument      `json:"notification_views,omitempty"`
	PushState            DevelopmentPushStateDocument              `json:"push_state,omitempty"`
	More                 bool                                      `json:"more,omitempty"`
}

func (s *Store) usesEventingBroker() bool {
	return s != nil && (s.broker != nil || s.brokerErr != nil)
}

func (s *Store) brokerCall(
	ctx context.Context,
	operation string,
	request eventingBrokerRequest,
	response *eventingBrokerResponse,
	mutation bool,
) error {
	if s == nil || s.broker == nil {
		if s != nil && s.brokerErr != nil {
			return s.brokerErr
		}
		return ErrClosed
	}
	if s.closed.Load() {
		return ErrClosed
	}
	request.StoreID = s.storeID
	var err error
	if mutation {
		err = s.broker.CallWithOptions(
			ctx,
			BrokerDomain,
			BrokerVersion,
			operation,
			request,
			response,
			database.CallOptions{Mutation: true},
		)
	} else {
		err = s.broker.Call(ctx, BrokerDomain, BrokerVersion, operation, request, response)
	}
	return decodeEventingBrokerError(err)
}

func mapEventingBrokerError(err error) error {
	if err == nil {
		return nil
	}
	var structured *database.Error
	if errors.As(err, &structured) && structured != nil {
		return database.NewError(structured.Code, structured.Message)
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return database.NewError(database.CodeDeadline, "eventing operation deadline was exceeded")
	case errors.Is(err, ErrSchemaTooNew):
		return database.NewError(database.CodeUnsupported, "eventing schema is newer than supported")
	case errors.Is(err, ErrSchemaInvalid):
		return database.NewError(database.CodeIntegrity, "eventing schema validation failed")
	case errors.Is(err, ErrNotFound):
		return database.NewError(database.CodeNotFound, "eventing_not_found")
	case errors.Is(err, ErrStaleLease):
		return database.NewError(database.CodeConflict, "eventing_stale_lease")
	case errors.Is(err, ErrInvalidTransition):
		return database.NewError(database.CodeConflict, "eventing_invalid_transition")
	case errors.Is(err, ErrRunIDMismatch):
		return database.NewError(database.CodeConflict, "eventing_run_id_mismatch")
	case errors.Is(err, ErrPayloadTooLarge):
		return database.NewError(database.CodeInvalid, "eventing_payload_too_large")
	case errors.Is(err, ErrInvalidEnvelope):
		return database.NewError(database.CodeInvalid, "eventing_invalid_envelope")
	case errors.Is(err, ErrInvalidPRWorkspace):
		return database.NewError(database.CodeInvalid, "eventing_invalid_pr_workspace")
	case errors.Is(err, ErrPRWorkspaceConflict):
		return database.NewError(database.CodeConflict, "eventing_pr_workspace_conflict")
	case errors.Is(err, developmentnotifications.ErrInvalidNotification):
		return database.NewError(database.CodeInvalid, "eventing_invalid_notification")
	case errors.Is(err, developmentnotifications.ErrInvalidTransition):
		return database.NewError(database.CodeConflict, "eventing_notification_transition")
	case errors.Is(err, developmentnotifications.ErrStaleGeneration):
		return database.NewError(database.CodeConflict, "eventing_notification_stale")
	case errors.Is(err, developmentnotifications.ErrInvalidSavedView):
		return database.NewError(database.CodeInvalid, "eventing_invalid_saved_view")
	case errors.Is(err, developmentnotifications.ErrStaleViewVersion):
		return database.NewError(database.CodeConflict, "eventing_saved_view_stale")
	case errors.Is(err, ErrClosed):
		return database.NewError(database.CodeUnavailable, "eventing_closed")
	default:
		return database.NewError(database.CodeInternal, "eventing broker operation failed")
	}
}

func decodeEventingBrokerError(err error) error {
	if err == nil {
		return nil
	}
	if database.CodeOf(err) == database.CodeOutcomeUnknown {
		return err
	}
	var value *database.Error
	if !errors.As(err, &value) {
		return err
	}
	switch value.Message {
	case "eventing_not_found":
		return ErrNotFound
	case "eventing_stale_lease":
		return ErrStaleLease
	case "eventing_invalid_transition":
		return ErrInvalidTransition
	case "eventing_run_id_mismatch":
		return ErrRunIDMismatch
	case "eventing_payload_too_large":
		return ErrPayloadTooLarge
	case "eventing_invalid_envelope":
		return ErrInvalidEnvelope
	case "eventing_invalid_pr_workspace":
		return ErrInvalidPRWorkspace
	case "eventing_pr_workspace_conflict":
		return ErrPRWorkspaceConflict
	case "eventing_invalid_notification":
		return developmentnotifications.ErrInvalidNotification
	case "eventing_notification_transition":
		return developmentnotifications.ErrInvalidTransition
	case "eventing_notification_stale":
		return developmentnotifications.ErrStaleGeneration
	case "eventing_invalid_saved_view":
		return developmentnotifications.ErrInvalidSavedView
	case "eventing_saved_view_stale":
		return developmentnotifications.ErrStaleViewVersion
	case "eventing_closed":
		return ErrClosed
	}
	if value.Code == database.CodeDeadline {
		return context.DeadlineExceeded
	}
	return err
}

type eventingBrokerWorkspace struct {
	selector        string
	databasePath    string
	maxPayloadBytes int
	redactFields    []string
	secretValues    []string

	once  sync.Once
	store *Store
	err   error
}

func (workspace *eventingBrokerWorkspace) open() (*Store, error) {
	if workspace == nil {
		return nil, database.NewError(database.CodeUnavailable, "eventing store is unavailable")
	}
	workspace.once.Do(func() {
		workspace.store, workspace.err = openLocal(
			context.Background(),
			workspace.databasePath,
			WithMaxPayloadBytes(workspace.maxPayloadBytes),
			WithRedaction(workspace.redactFields, workspace.secretValues),
		)
	})
	return workspace.store, workspace.err
}

// BrokerHandler owns one stable eventing pool for the primary and every
// distinct configured-agent workspace. Runtime paths never cross the broker
// boundary; only one-way selectors and cataloged StoreIDs do.
type BrokerHandler struct {
	mu sync.RWMutex

	workspaces map[database.StoreID]*eventingBrokerWorkspace
	selectors  map[string]database.StoreID

	// Primary aliases are retained for one-package compatibility tests.
	storeID          database.StoreID
	store            *Store
	primaryStoreOnce sync.Once
	closed           bool

	closeOnce sync.Once
	closeErr  error
}

func NewBrokerHandler(home string, cfg *config.Config) (*BrokerHandler, error) {
	if cfg == nil {
		return nil, database.NewError(database.CodeInvalid, "eventing broker configuration is invalid")
	}
	catalog, err := dbcatalog.New(home, cfg)
	if err != nil {
		return nil, err
	}
	configured, err := configuredEventingWorkspaces(home, cfg, catalog)
	if err != nil {
		return nil, err
	}
	handler := &BrokerHandler{
		workspaces: make(map[database.StoreID]*eventingBrokerWorkspace, len(configured)),
		selectors:  make(map[string]database.StoreID, len(configured)),
	}
	secretValues := append([]string(nil), cfg.SensitiveDataValues()...)
	for index, item := range configured {
		workspace := &eventingBrokerWorkspace{
			selector:        item.selector,
			databasePath:    item.databasePath,
			maxPayloadBytes: item.maxPayloadBytes,
			redactFields:    append([]string(nil), item.redactFields...),
			secretValues:    append([]string(nil), secretValues...),
		}
		handler.workspaces[item.storeID] = workspace
		handler.selectors[item.selector] = item.storeID
		handler.selectors[item.workspaceSelect] = item.storeID
		if index == 0 {
			handler.storeID = item.storeID
		}
	}
	return handler, nil
}

func (handler *BrokerHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if handler == nil || request.Domain != BrokerDomain || request.Version != BrokerVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, database.NewError(database.CodeDeadline, "eventing operation deadline was exceeded")
	}
	handler.mu.RLock()
	defer handler.mu.RUnlock()
	if handler.closed {
		return nil, database.NewError(database.CodeUnavailable, "eventing broker is unavailable")
	}
	if request.Operation == eventingOpResolveStore {
		var input eventingResolveRequest
		if request.DecodePayload(&input) != nil || !validEventingSelector(input.WorkspaceSelector) {
			return nil, database.NewError(database.CodeInvalid, "eventing broker request is invalid")
		}
		storeID, ok := handler.selectors[input.WorkspaceSelector]
		if !ok {
			return nil, database.NewError(database.CodeUnauthorized, "eventing workspace is not cataloged")
		}
		return eventingResolveResponse{StoreID: storeID}, nil
	}
	if request.Operation == BrokerPreflightOperation {
		var input eventingBrokerTarget
		if request.DecodePayload(&input) != nil || !input.StoreID.Valid() {
			return nil, database.NewError(database.CodeInvalid, "eventing broker request is invalid")
		}
		workspace := handler.workspaces[input.StoreID]
		if workspace == nil {
			return nil, database.NewError(database.CodeUnauthorized, "eventing store is not cataloged")
		}
		store, err := workspace.open()
		if err != nil {
			return nil, mapEventingBrokerError(err)
		}
		handler.retainPrimaryAlias(input.StoreID, store)
		return eventingBrokerResponse{Ready: true}, nil
	}
	var input eventingBrokerRequest
	if err := request.DecodePayload(&input); err != nil {
		return nil, database.NewError(database.CodeInvalid, "eventing broker request is invalid")
	}
	if !input.StoreID.Valid() {
		return nil, database.NewError(database.CodeInvalid, "eventing broker request is invalid")
	}
	workspace, ok := handler.workspaces[input.StoreID]
	if !ok || workspace == nil {
		return nil, database.NewError(database.CodeUnauthorized, "eventing store is not cataloged")
	}
	store, err := workspace.open()
	if err != nil {
		return nil, mapEventingBrokerError(err)
	}
	handler.retainPrimaryAlias(input.StoreID, store)
	response, err := handler.dispatch(ctx, request.Operation, input, store)
	return response, mapEventingBrokerError(err)
}

func (handler *BrokerHandler) retainPrimaryAlias(storeID database.StoreID, store *Store) {
	if handler != nil && storeID == handler.storeID {
		handler.primaryStoreOnce.Do(func() { handler.store = store })
	}
}

// dispatch keeps every related mutation inside one broker-side store command.
func (handler *BrokerHandler) dispatch(
	ctx context.Context,
	operation string,
	in eventingBrokerRequest,
	s *Store,
) (eventingBrokerResponse, error) {
	switch operation {
	case eventingOpInsert:
		value, err := s.Insert(ctx, in.Envelope)
		return eventingBrokerResponse{Insert: value}, err
	case eventingOpGet:
		value, err := s.Get(ctx, in.ID)
		return eventingBrokerResponse{Event: value}, err
	case eventingOpList:
		value, err := s.List(ctx, in.EventFilter)
		return eventingBrokerResponse{EventPage: value}, err
	case eventingOpGetEventMetadata:
		value, err := s.GetEventMetadata(ctx, in.ID)
		return eventingBrokerResponse{EventMetadata: value}, err
	case eventingOpGetEventPayload:
		value, err := s.GetEventPayload(ctx, in.ID)
		return eventingBrokerResponse{EventPayload: value}, err
	case eventingOpListEventMetadata:
		value, err := s.ListEventMetadata(ctx, in.EventFilter)
		return eventingBrokerResponse{EventMetadataPage: value}, err
	case eventingOpClaimRouting:
		value, err := s.ClaimRouting(ctx, in.WorkerLabel, in.Limit, in.Lease)
		return eventingBrokerResponse{Events: value}, err
	case eventingOpRenewRouting:
		return eventingBrokerResponse{Mutation: true}, s.RenewRoutingLease(ctx, in.ID, in.LeaseToken, in.Lease)
	case eventingOpAckRouting:
		return eventingBrokerResponse{Mutation: true}, s.AckRouting(ctx, in.ID, in.LeaseToken)
	case eventingOpNackRouting:
		return eventingBrokerResponse{
				Mutation: true,
			}, s.NackRouting(
				ctx,
				in.ID,
				in.LeaseToken,
				in.AvailableAt,
				in.Detail,
			)
	case eventingOpDeadRouting:
		return eventingBrokerResponse{Mutation: true}, s.DeadRouting(ctx, in.ID, in.LeaseToken, in.Detail)
	case eventingOpCreateDispatchClaim:
		value, created, err := s.CreateDispatchForRoutingClaim(ctx, in.ID, in.LeaseToken, in.WorkflowRef)
		return eventingBrokerResponse{Dispatch: value, DispatchCreated: created}, err
	case eventingOpCreateRevisionedDispatchClaim:
		value, created, err := s.CreateRevisionedDispatchForRoutingClaim(
			ctx,
			in.ID,
			in.LeaseToken,
			in.WorkflowRef,
			in.WorkflowRevision,
		)
		return eventingBrokerResponse{Dispatch: value, DispatchCreated: created}, err
	case eventingOpCreateDispatch:
		value, created, err := s.CreateDispatch(ctx, in.ID, in.WorkflowRef)
		return eventingBrokerResponse{Dispatch: value, DispatchCreated: created}, err
	case eventingOpGetDispatch:
		value, err := s.GetDispatch(ctx, in.ID)
		return eventingBrokerResponse{Dispatch: value}, err
	case eventingOpGetDispatchMetadata:
		value, err := s.GetDispatchMetadata(ctx, in.ID)
		return eventingBrokerResponse{DispatchMetadata: value}, err
	case eventingOpClaimDispatches:
		value, err := s.ClaimDispatches(ctx, in.WorkerLabel, in.Limit, in.Lease)
		return eventingBrokerResponse{Dispatches: value}, err
	case eventingOpLinkDispatchRun:
		return eventingBrokerResponse{Mutation: true}, s.LinkDispatchRun(ctx, in.ID, in.LeaseToken, in.RunID)
	case eventingOpRenewDispatch:
		return eventingBrokerResponse{Mutation: true}, s.RenewDispatchLease(ctx, in.ID, in.LeaseToken, in.Lease)
	case eventingOpFinishDispatch:
		return eventingBrokerResponse{
				Mutation: true,
			}, s.FinishDispatch(
				ctx,
				in.ID,
				in.LeaseToken,
				in.DispatchStatus,
				in.Detail,
			)
	case eventingOpNackDispatch:
		return eventingBrokerResponse{
				Mutation: true,
			}, s.NackDispatch(
				ctx,
				in.ID,
				in.LeaseToken,
				in.AvailableAt,
				in.Detail,
			)
	case eventingOpListDispatches:
		value, err := s.ListDispatches(ctx, in.DispatchFilter)
		return eventingBrokerResponse{DispatchPage: value}, err
	case eventingOpListDispatchMetadata:
		value, err := s.ListDispatchMetadata(ctx, in.DispatchFilter)
		return eventingBrokerResponse{DispatchMetadataPage: value}, err
	case eventingOpReplay:
		value, err := s.Replay(ctx, in.ID)
		return eventingBrokerResponse{Insert: value}, err
	case eventingOpPrune:
		value, err := s.Prune(ctx, in.Before, in.Limit)
		return eventingBrokerResponse{Count: value}, err
	case eventingOpSetPRCutover:
		return eventingBrokerResponse{Mutation: true}, s.SetPRWorkspaceIngressCutover(ctx, in.Watermark)
	case eventingOpGetPRCutover:
		value, err := s.GetPRWorkspaceIngressCutover(ctx, in.Source, in.Connector)
		return eventingBrokerResponse{Watermark: value}, err
	case eventingOpCreatePRWorkspace:
		value, created, err := s.CreatePRWorkspace(ctx, in.PRCreate)
		return eventingBrokerResponse{PRAggregate: value, PRCreated: created}, err
	case eventingOpGetPRWorkspace:
		value, err := s.GetPRWorkspace(ctx, in.WorkspaceID)
		return eventingBrokerResponse{PRAggregate: value}, err
	case eventingOpListPRWorkspaces:
		value, err := s.ListPRWorkspaces(ctx, in.PRFilter)
		return eventingBrokerResponse{PRPage: value}, err
	case eventingOpApplyPRMutation:
		value, err := s.ApplyPRWorkspaceMutation(ctx, in.PRMutation)
		return eventingBrokerResponse{PRMutationResult: value}, err
	case eventingOpApplyPRPatch:
		value, err := s.ApplyPRWorkspacePatch(ctx, in.PRPatch)
		return eventingBrokerResponse{PRPatchResult: value}, err
	case eventingOpClaimPROperations:
		value, err := s.ClaimPRWorkspaceOperations(ctx, in.PRClaim)
		return eventingBrokerResponse{PROperations: value}, err
	case eventingOpFinishPROperation:
		value, err := s.FinishPRWorkspaceOperation(ctx, in.PROperationFinish)
		return eventingBrokerResponse{PROperation: value}, err
	case eventingOpClaimPRPublications:
		value, err := s.ClaimPRWorkspacePublications(ctx, in.PRClaim)
		return eventingBrokerResponse{PRPublications: value}, err
	case eventingOpFinishPRPublication:
		value, err := s.FinishPRWorkspacePublication(ctx, in.PRPublicationFinish)
		return eventingBrokerResponse{PRPublication: value}, err
	case eventingOpUpsertNotification:
		value, err := s.UpsertDevelopmentNotification(ctx, in.NotificationDraft)
		return eventingBrokerResponse{NotificationUpsert: value}, err
	case eventingOpListNotifications:
		if in.Offset < 0 {
			return eventingBrokerResponse{}, database.NewError(
				database.CodeInvalid,
				"eventing broker request is invalid",
			)
		}
		values, more, err := s.listDevelopmentNotificationsPage(ctx, in.Offset, eventingNotificationPageSize)
		return eventingBrokerResponse{Notifications: values, More: more}, err
	case eventingOpListPushNotifications:
		value, err := s.ListRecentDevelopmentPushNotifications(ctx, in.Limit)
		return eventingBrokerResponse{Notifications: value}, err
	case eventingOpGetNotification:
		value, err := s.GetDevelopmentNotification(ctx, in.ID)
		return eventingBrokerResponse{Notification: value}, err
	case eventingOpMutateNotification:
		value, err := s.MutateDevelopmentNotification(ctx, in.ID, in.ExpectedVersion, in.Action, in.SnoozedUntil)
		return eventingBrokerResponse{Notification: value}, err
	case eventingOpMutateNotifications:
		value, err := s.MutateDevelopmentNotifications(ctx, in.NotificationBulk)
		return eventingBrokerResponse{NotificationBulk: value}, err
	case eventingOpGetNotificationViews:
		value, err := s.GetDevelopmentNotificationViews(ctx)
		return eventingBrokerResponse{NotificationViews: value}, err
	case eventingOpPutNotificationViews:
		value, err := s.PutDevelopmentNotificationViews(ctx, in.Views, in.ExpectedVersion)
		return eventingBrokerResponse{NotificationViews: value}, err
	case eventingOpGetPushState:
		value, err := s.GetDevelopmentPushState(ctx)
		return eventingBrokerResponse{PushState: value}, err
	case eventingOpPutPushState:
		value, err := s.PutDevelopmentPushState(ctx, in.PushState, in.ExpectedVersion)
		return eventingBrokerResponse{PushState: value}, err
	case eventingOpPruneNotifications:
		value, err := s.PruneDevelopmentNotifications(ctx, in.Before, in.Limit)
		return eventingBrokerResponse{Count: value}, err
	default:
		return eventingBrokerResponse{}, database.NewError(
			database.CodeUnsupported,
			"eventing operation is unsupported",
		)
	}
}

func (s *Store) listDevelopmentNotificationsPage(
	ctx context.Context,
	offset, limit int,
) ([]developmentnotifications.Notification, bool, error) {
	if err := s.ready(ctx); err != nil {
		return nil, false, err
	}
	if offset < 0 {
		offset = 0
	}
	if limit < 1 || limit > eventingNotificationPageSize {
		limit = eventingNotificationPageSize
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload_json FROM development_notifications
		ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`, limit+1, offset)
	if err != nil {
		return nil, false, s.dbError(err)
	}
	defer rows.Close()
	values := make([]developmentnotifications.Notification, 0, limit+1)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, false, s.dbError(err)
		}
		var value developmentnotifications.Notification
		if err := json.Unmarshal(payload, &value); err != nil || value.Validate() != nil {
			return nil, false, errors.New("development notification payload is invalid")
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, false, s.dbError(err)
	}
	more := len(values) > limit
	if more {
		values = values[:limit]
	}
	return values, more, nil
}

func (handler *BrokerHandler) Close() error {
	if handler == nil {
		return nil
	}
	handler.closeOnce.Do(func() {
		handler.mu.Lock()
		defer handler.mu.Unlock()
		handler.closed = true
		for _, workspace := range handler.workspaces {
			if workspace != nil && workspace.store != nil {
				handler.closeErr = errors.Join(handler.closeErr, workspace.store.Close())
			}
		}
	})
	return handler.closeErr
}

type configuredEventingWorkspace struct {
	databasePath    string
	selector        string
	workspaceSelect string
	storeID         database.StoreID
	maxPayloadBytes int
	redactFields    []string
}

func configuredEventingWorkspaces(
	home string,
	cfg *config.Config,
	catalog *dbcatalog.Catalog,
) ([]configuredEventingWorkspace, error) {
	canonicalHome, err := database.CanonicalHome(home)
	if err != nil {
		return nil, err
	}
	primary, err := resolveConfiguredEventingWorkspace(canonicalHome, cfg.Agents.Defaults.Workspace)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		workspace string
		primary   bool
	}
	candidates := []candidate{{workspace: primary, primary: true}}
	for _, agent := range cfg.Agents.List {
		if strings.TrimSpace(agent.Workspace) == "" {
			continue
		}
		workspace, resolveErr := resolveConfiguredEventingWorkspace(canonicalHome, agent.Workspace)
		if resolveErr != nil {
			return nil, resolveErr
		}
		candidates = append(candidates, candidate{workspace: workspace})
	}
	policy := config.EffectiveEventIngressConfig(cfg, primary)
	seenWorkspaces := make(map[string]struct{}, len(candidates))
	seenSelectors := make(map[string]string, len(candidates))
	result := make([]configuredEventingWorkspace, 0, len(candidates))
	for _, item := range candidates {
		if _, duplicate := seenWorkspaces[item.workspace]; duplicate {
			continue
		}
		seenWorkspaces[item.workspace] = struct{}{}
		ingress := policy
		if !item.primary {
			dynamic := config.EffectiveEventIngressConfig(nil, item.workspace)
			ingress.DatabasePath = dynamic.DatabasePath
		}
		databasePath, pathErr := canonicalEventingTargetPath(ingress.DatabasePath)
		if pathErr != nil {
			return nil, pathErr
		}
		selector, selectorErr := eventingWorkspaceSelector(databasePath)
		if selectorErr != nil {
			return nil, selectorErr
		}
		if previous, collision := seenSelectors[selector]; collision && previous != databasePath {
			return nil, database.NewError(database.CodeIntegrity, "eventing workspace selector collides")
		}
		seenSelectors[selector] = databasePath
		workspaceSelector, workspaceErr := eventingWorkspaceIDSelector(item.workspace)
		if workspaceErr != nil {
			return nil, workspaceErr
		}
		if previous, collision := seenSelectors[workspaceSelector]; collision && previous != databasePath {
			return nil, database.NewError(database.CodeIntegrity, "eventing workspace selector collides")
		}
		seenSelectors[workspaceSelector] = databasePath
		logicalName := EventingStoreID.String()
		if !item.primary {
			logicalName = "workspace." + workspaceSelector + ".eventing"
		}
		storeID, lookupErr := catalog.Lookup(logicalName)
		if lookupErr != nil {
			return nil, database.NewError(database.CodeIntegrity, "eventing store is missing from the catalog")
		}
		result = append(result, configuredEventingWorkspace{
			databasePath:    databasePath,
			selector:        selector,
			workspaceSelect: workspaceSelector,
			storeID:         storeID,
			maxPayloadBytes: ingress.MaxPayloadBytes,
			redactFields:    append([]string(nil), ingress.RedactFields...),
		})
	}
	return result, nil
}

func resolveConfiguredEventingWorkspace(home, configured string) (string, error) {
	value := strings.TrimSpace(configured)
	if value == "" {
		value = filepath.Join(home, "workspace")
	} else if value == "~" || strings.HasPrefix(value, "~/") {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if value == "~" {
			value = userHome
		} else {
			value = filepath.Join(userHome, value[2:])
		}
	} else if !filepath.IsAbs(value) {
		value = filepath.Join(home, value)
	}
	resolved, err := canonicalEventingPathPrefix(value)
	if err != nil {
		return "", database.NewError(database.CodeInvalid, "eventing workspace is invalid")
	}
	return resolved, nil
}

func canonicalEventingTargetPath(path string) (string, error) {
	if path == "" || path != strings.TrimSpace(path) || strings.ContainsRune(path, 0) || path == ":memory:" {
		return "", database.NewError(database.CodeInvalid, "eventing workspace is invalid")
	}
	return canonicalEventingPathPrefix(path)
}

func canonicalEventingPathPrefix(path string) (string, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	probe := absolute
	var suffix []string
	for {
		if _, statErr := os.Lstat(probe); statErr == nil {
			resolved, resolveErr := filepath.EvalSymlinks(probe)
			if resolveErr != nil {
				return "", resolveErr
			}
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		} else if errors.Is(statErr, os.ErrNotExist) {
			parent := filepath.Dir(probe)
			if parent == probe {
				return absolute, nil
			}
			suffix = append(suffix, filepath.Base(probe))
			probe = parent
		} else {
			return "", statErr
		}
	}
}

func eventingWorkspaceSelector(databasePath string) (string, error) {
	canonical, err := canonicalEventingTargetPath(databasePath)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(filepath.Clean(canonical)))
	return fmt.Sprintf("%x", digest[:8]), nil
}

func eventingWorkspaceIDSelector(workspace string) (string, error) {
	canonical, err := canonicalEventingPathPrefix(workspace)
	if err != nil {
		return "", database.NewError(database.CodeInvalid, "eventing workspace is invalid")
	}
	digest := sha256.Sum256([]byte(filepath.Clean(canonical)))
	return fmt.Sprintf("%x", digest[:8]), nil
}

func validEventingSelector(selector string) bool {
	if len(selector) != 16 {
		return false
	}
	for _, value := range selector {
		if (value < '0' || value > '9') && (value < 'a' || value > 'f') {
			return false
		}
	}
	return true
}

var _ database.Handler = (*BrokerHandler)(nil)
