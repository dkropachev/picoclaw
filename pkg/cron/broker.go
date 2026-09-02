package cron

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
	"unicode/utf8"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	dbcatalog "github.com/sipeed/picoclaw/pkg/database/catalog"
)

const (
	// BrokerDomain is the typed cron domain registered by the database broker.
	BrokerDomain  = "cron"
	BrokerVersion = 1

	BrokerStoreID database.StoreID = "workspace.cron"

	cronBrokerPageItems = 128
	cronBrokerPageBytes = 8 << 20
)

const (
	cronOperationList       = "list"
	cronOperationGet        = "get"
	cronOperationAdd        = "add"
	cronOperationUpdate     = "update"
	cronOperationRemove     = "remove"
	cronOperationEnable     = "enable"
	cronOperationStatus     = "status"
	cronOperationInitialize = "initialize-scheduler"
	cronOperationClaim      = "claim-due"
	cronOperationComplete   = "complete-run"
	cronOperationResolve    = "resolve-store"
	cronOperationPreflight  = "preflight"
)

type cronResolveStoreRequest struct {
	WorkspaceSelector string `json:"workspace_selector"`
}

type cronResolveStoreResponse struct {
	StoreID database.StoreID `json:"store_id"`
}

type cronStoreRequest struct {
	StoreID database.StoreID `json:"store_id"`
}

type cronListRequest struct {
	StoreID         database.StoreID `json:"store_id"`
	IncludeDisabled bool             `json:"include_disabled"`
	Cursor          int              `json:"cursor"`
}

type cronJobRequest struct {
	StoreID database.StoreID `json:"store_id"`
	JobID   string           `json:"job_id"`
}

type cronAddRequest struct {
	StoreID  database.StoreID `json:"store_id"`
	Name     string           `json:"name"`
	Schedule CronSchedule     `json:"schedule"`
	Message  string           `json:"message"`
	Channel  string           `json:"channel"`
	To       string           `json:"to"`
}

type cronUpdateRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Job     *CronJob         `json:"job"`
}

type cronEnableRequest struct {
	StoreID database.StoreID `json:"store_id"`
	JobID   string           `json:"job_id"`
	Enabled bool             `json:"enabled"`
}

type cronStatusRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Running bool             `json:"running"`
}

type cronClaimRequest struct {
	StoreID database.StoreID `json:"store_id"`
	JobID   string           `json:"job_id"`
	NowMS   int64            `json:"now_ms"`
}

type cronCompleteRequest struct {
	StoreID    database.StoreID `json:"store_id"`
	JobID      string           `json:"job_id"`
	StartedMS  int64            `json:"started_ms"`
	FinishedMS int64            `json:"finished_ms"`
	Succeeded  bool             `json:"succeeded"`
	Failure    string           `json:"failure,omitempty"`
}

type cronStatusResponse struct {
	Enabled      bool   `json:"enabled"`
	Jobs         int    `json:"jobs"`
	NextWakeAtMS *int64 `json:"next_wake_at_ms,omitempty"`
}

type cronBrokerResponse struct {
	Jobs       []CronJob           `json:"jobs,omitempty"`
	Job        *CronJob            `json:"job,omitempty"`
	Found      bool                `json:"found,omitempty"`
	Removed    bool                `json:"removed,omitempty"`
	Claimed    bool                `json:"claimed,omitempty"`
	JobName    string              `json:"job_name,omitempty"`
	NextRun    string              `json:"next_run,omitempty"`
	Status     *cronStatusResponse `json:"status,omitempty"`
	NextCursor int                 `json:"next_cursor,omitempty"`
	Done       bool                `json:"done,omitempty"`
}

type cronBrokerWorkspace struct {
	selector  string
	storePath string
	service   *CronService
	opMu      sync.Mutex
}

// BrokerHandler owns one stable CronService/pool for the primary and every
// distinct configured-agent workspace.
type BrokerHandler struct {
	mu         sync.RWMutex
	workspaces map[database.StoreID]*cronBrokerWorkspace
	selectors  map[string]database.StoreID

	// Primary alias retained for one-package compatibility tests.
	serviceMu sync.RWMutex
	service   *CronService

	closed    bool
	closeOnce sync.Once
	closeErr  error
}

// NewBrokerHandler catalogs local-only services derived solely from trusted
// configuration. Each store opens lazily on its first target-specific request;
// construction never consults RuntimeClient or opens a SQLite generation.
func NewBrokerHandler(home string, cfg *config.Config) (*BrokerHandler, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() &&
		!allowUnfencedCronProviderForTests.Load() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"cron broker handler requires online database fencing",
		)
	}
	if cfg == nil {
		return nil, database.NewError(database.CodeInvalid, "cron broker configuration is invalid")
	}
	catalog, err := dbcatalog.New(home, cfg)
	if err != nil {
		return nil, err
	}
	configured, err := configuredCronWorkspaces(home, cfg, catalog)
	if err != nil {
		return nil, err
	}
	handler := &BrokerHandler{
		workspaces: make(map[database.StoreID]*cronBrokerWorkspace, len(configured)),
		selectors:  make(map[string]database.StoreID, len(configured)),
	}
	for _, item := range configured {
		workspace := &cronBrokerWorkspace{
			selector:  item.selector,
			storePath: filepath.Join(item.workspace, "cron"),
		}
		handler.workspaces[item.storeID] = workspace
		handler.selectors[item.selector] = item.storeID
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
		return nil, database.NewError(database.CodeDeadline, "cron request deadline was exceeded")
	}
	handler.mu.RLock()
	defer handler.mu.RUnlock()
	if handler.closed {
		return nil, database.NewError(database.CodeUnavailable, "cron broker handler is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, database.NewError(database.CodeDeadline, "cron request deadline was exceeded")
	}

	if request.Operation == cronOperationResolve {
		var input cronResolveStoreRequest
		if request.DecodePayload(&input) != nil || !validCronWorkspaceSelector(input.WorkspaceSelector) {
			return nil, invalidCronRequest()
		}
		storeID, ok := handler.selectors[input.WorkspaceSelector]
		if !ok {
			return nil, database.NewError(database.CodeUnauthorized, "cron workspace is not cataloged")
		}
		return cronResolveStoreResponse{StoreID: storeID}, nil
	}
	storeID, err := cronRequestStoreID(request)
	if err != nil {
		return nil, err
	}
	workspace, ok := handler.workspaces[storeID]
	if !ok || workspace == nil {
		return nil, database.NewError(database.CodeUnauthorized, "cron store is not cataloged")
	}
	if request.Operation == cronOperationPreflight {
		var input cronStoreRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != storeID {
			return nil, invalidCronRequest()
		}
	}
	workspace.opMu.Lock()
	defer workspace.opMu.Unlock()
	if workspace.service == nil {
		service, openErr := newLocalCronService(workspace.storePath, nil)
		if openErr != nil {
			if service != nil {
				_ = service.Close()
			}
			return nil, mapCronBrokerError(openErr)
		}
		service.storeID = storeID
		workspace.service = service
		if storeID == BrokerStoreID {
			handler.serviceMu.Lock()
			handler.service = service
			handler.serviceMu.Unlock()
		}
	}
	service := workspace.service

	switch request.Operation {
	case cronOperationPreflight:
		return handler.response(nil, nil), nil
	case cronOperationList:
		var input cronListRequest
		if err := request.DecodePayload(&input); err != nil || !validCronStoreID(input.StoreID) ||
			input.Cursor < 0 || input.Cursor > maximumCronJobs {
			return nil, invalidCronRequest()
		}
		jobs := service.ListJobs(input.IncludeDisabled)
		if service.initErr != nil {
			return nil, mapCronBrokerError(service.initErr)
		}
		if input.Cursor > len(jobs) {
			return nil, invalidCronRequest()
		}
		response := cronBrokerResponse{
			Jobs: make([]CronJob, 0, min(cronBrokerPageItems, len(jobs)-input.Cursor)),
		}
		pageBytes := 0
		index := input.Cursor
		for ; index < len(jobs) && len(response.Jobs) < cronBrokerPageItems; index++ {
			raw, err := database.MarshalCanonical(jobs[index])
			if err != nil || len(raw) > cronBrokerPageBytes {
				return nil, database.NewError(database.CodeIntegrity, "cron job exceeds list page limit")
			}
			if len(response.Jobs) > 0 && pageBytes+len(raw) > cronBrokerPageBytes {
				break
			}
			response.Jobs = append(response.Jobs, cloneCronJob(jobs[index]))
			pageBytes += len(raw)
		}
		response.NextCursor = index
		response.Done = index == len(jobs)
		return response, nil
	case cronOperationGet:
		var input cronJobRequest
		if err := request.DecodePayload(&input); err != nil || !validCronStoreID(input.StoreID) ||
			!validCronJobID(input.JobID) {
			return nil, invalidCronRequest()
		}
		job, found := service.GetJob(input.JobID)
		if service.initErr != nil {
			return nil, mapCronBrokerError(service.initErr)
		}
		response := handler.response(nil, job)
		response.Found = found
		return response, nil
	case cronOperationAdd:
		var input cronAddRequest
		if err := request.DecodePayload(&input); err != nil || !validCronStoreID(input.StoreID) ||
			validateCronAddInput(input) != nil {
			return nil, invalidCronRequest()
		}
		job, err := service.AddJob(
			input.Name, input.Schedule, input.Message, input.Channel, input.To,
		)
		if err != nil {
			return nil, mapCronBrokerError(err)
		}
		return handler.response(nil, job), nil
	case cronOperationUpdate:
		var input cronUpdateRequest
		if err := request.DecodePayload(&input); err != nil || !validCronStoreID(input.StoreID) ||
			input.Job == nil || validateCronJob(input.Job) != nil {
			return nil, invalidCronRequest()
		}
		if err := service.UpdateJob(input.Job); err != nil {
			if isCronNotFound(err) {
				return nil, database.NewError(database.CodeNotFound, "cron job was not found")
			}
			return nil, mapCronBrokerError(err)
		}
		job, _ := service.GetJob(input.Job.ID)
		return handler.response(nil, job), nil
	case cronOperationRemove:
		var input cronJobRequest
		if err := request.DecodePayload(&input); err != nil || !validCronStoreID(input.StoreID) ||
			!validCronJobID(input.JobID) {
			return nil, invalidCronRequest()
		}
		removed := service.RemoveJob(input.JobID)
		if service.initErr != nil {
			return nil, mapCronBrokerError(service.initErr)
		}
		response := handler.response(nil, nil)
		response.Removed = removed
		return response, nil
	case cronOperationEnable:
		var input cronEnableRequest
		if err := request.DecodePayload(&input); err != nil || !validCronStoreID(input.StoreID) ||
			!validCronJobID(input.JobID) {
			return nil, invalidCronRequest()
		}
		job := service.EnableJob(input.JobID, input.Enabled)
		if service.initErr != nil {
			return nil, mapCronBrokerError(service.initErr)
		}
		response := handler.response(nil, job)
		response.Found = job != nil
		return response, nil
	case cronOperationStatus:
		var input cronStatusRequest
		if err := request.DecodePayload(&input); err != nil || !validCronStoreID(input.StoreID) {
			return nil, invalidCronRequest()
		}
		status, err := service.statusSnapshot(input.Running)
		if err != nil {
			return nil, mapCronBrokerError(err)
		}
		return cronBrokerResponse{Status: &status}, nil
	case cronOperationInitialize:
		var input cronStoreRequest
		if err := request.DecodePayload(&input); err != nil || !validCronStoreID(input.StoreID) {
			return nil, invalidCronRequest()
		}
		if err := service.initializeSchedulerStore(); err != nil {
			return nil, mapCronBrokerError(err)
		}
		return handler.response(nil, nil), nil
	case cronOperationClaim:
		var input cronClaimRequest
		if err := request.DecodePayload(&input); err != nil || !validCronStoreID(input.StoreID) ||
			!validCronJobID(input.JobID) || input.NowMS < 0 {
			return nil, invalidCronRequest()
		}
		claimed, err := service.claimDueJob(input.JobID, input.NowMS)
		if err != nil {
			return nil, mapCronBrokerError(err)
		}
		response := handler.response(nil, nil)
		response.Claimed = claimed
		return response, nil
	case cronOperationComplete:
		var input cronCompleteRequest
		if err := request.DecodePayload(&input); err != nil || !validCronStoreID(input.StoreID) ||
			!validCronJobID(input.JobID) || input.StartedMS < 0 || input.FinishedMS < input.StartedMS ||
			len(input.Failure) > maximumCronErrorBytes || !utf8.ValidString(input.Failure) ||
			strings.ContainsRune(input.Failure, 0) {
			return nil, invalidCronRequest()
		}
		result, err := service.completeJob(input)
		if err != nil {
			return nil, mapCronBrokerError(err)
		}
		response := handler.response(nil, nil)
		response.Found = result.found
		response.JobName = result.jobName
		response.NextRun = result.nextRun
		return response, nil
	default:
		return nil, database.NewError(database.CodeUnsupported, "cron operation is unsupported")
	}
}

func (handler *BrokerHandler) response(jobs []CronJob, job *CronJob) cronBrokerResponse {
	response := cronBrokerResponse{}
	if jobs != nil {
		response.Jobs = make([]CronJob, len(jobs))
		for index := range jobs {
			response.Jobs[index] = cloneCronJob(jobs[index])
		}
	}
	if job != nil {
		jobCopy := cloneCronJob(*job)
		response.Job = &jobCopy
	}
	return response
}

// Close releases every broker-owned service/pool exactly once.
func (handler *BrokerHandler) Close() error {
	if handler == nil {
		return nil
	}
	handler.closeOnce.Do(func() {
		handler.mu.Lock()
		defer handler.mu.Unlock()
		handler.closed = true
		for _, workspace := range handler.workspaces {
			if workspace != nil && workspace.service != nil {
				handler.closeErr = errors.Join(handler.closeErr, workspace.service.Close())
			}
		}
	})
	return handler.closeErr
}

type configuredCronWorkspace struct {
	workspace string
	selector  string
	storeID   database.StoreID
}

func configuredCronWorkspaces(
	home string,
	cfg *config.Config,
	catalog *dbcatalog.Catalog,
) ([]configuredCronWorkspace, error) {
	canonicalHome, err := database.CanonicalHome(home)
	if err != nil {
		return nil, err
	}
	primary, err := resolveConfiguredCronWorkspace(canonicalHome, cfg.Agents.Defaults.Workspace)
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
		workspace, resolveErr := resolveConfiguredCronWorkspace(canonicalHome, agent.Workspace)
		if resolveErr != nil {
			return nil, resolveErr
		}
		candidates = append(candidates, candidate{workspace: workspace})
	}
	seenWorkspaces := make(map[string]struct{}, len(candidates))
	seenSelectors := make(map[string]string, len(candidates))
	result := make([]configuredCronWorkspace, 0, len(candidates))
	for _, item := range candidates {
		if _, duplicate := seenWorkspaces[item.workspace]; duplicate {
			continue
		}
		seenWorkspaces[item.workspace] = struct{}{}
		selector, selectorErr := cronWorkspaceSelector(item.workspace)
		if selectorErr != nil {
			return nil, selectorErr
		}
		if previous, collision := seenSelectors[selector]; collision && previous != item.workspace {
			return nil, database.NewError(database.CodeIntegrity, "cron workspace selector collides")
		}
		seenSelectors[selector] = item.workspace
		logicalName := BrokerStoreID.String()
		if !item.primary {
			logicalName = "workspace." + selector + ".cron"
		}
		storeID, lookupErr := catalog.Lookup(logicalName)
		if lookupErr != nil {
			return nil, database.NewError(database.CodeIntegrity, "cron store is missing from the catalog")
		}
		result = append(result, configuredCronWorkspace{
			workspace: item.workspace,
			selector:  selector,
			storeID:   storeID,
		})
	}
	return result, nil
}

func resolveConfiguredCronWorkspace(home, configured string) (string, error) {
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
	return canonicalCronWorkspace(value)
}

func resolveCronBrokerStoreID(
	ctx context.Context,
	client *database.Client,
	locator string,
) (database.StoreID, error) {
	if client == nil {
		return "", database.NewError(database.CodeUnavailable, "cron broker client is unavailable")
	}
	workspace, err := cronWorkspaceFromLocator(locator)
	if err != nil {
		return "", err
	}
	selector, err := cronWorkspaceSelector(workspace)
	if err != nil {
		return "", err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var response cronResolveStoreResponse
	err = client.Call(
		ctx, BrokerDomain, BrokerVersion, cronOperationResolve,
		cronResolveStoreRequest{WorkspaceSelector: selector}, &response,
	)
	if err != nil {
		return "", err
	}
	if !response.StoreID.Valid() {
		return "", database.NewError(database.CodeIntegrity, "cron broker StoreID is invalid")
	}
	return response.StoreID, nil
}

func cronWorkspaceFromLocator(locator string) (string, error) {
	value := strings.TrimSpace(locator)
	if value == "" || value != locator || strings.ContainsRune(value, 0) {
		return "", database.NewError(database.CodeInvalid, "cron workspace is invalid")
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", database.NewError(database.CodeInvalid, "cron workspace is invalid")
		}
		if value == "~" {
			value = userHome
		} else {
			value = filepath.Join(userHome, value[2:])
		}
	}
	if !filepath.IsAbs(value) {
		home, err := database.CanonicalHome(config.GetHome())
		if err != nil {
			return "", database.NewError(database.CodeInvalid, "cron workspace is invalid")
		}
		value = filepath.Join(home, value)
	}
	absolute, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", database.NewError(database.CodeInvalid, "cron workspace is invalid")
	}
	cronDir := absolute
	switch strings.ToLower(filepath.Ext(absolute)) {
	case "":
		if filepath.Base(absolute) != "cron" {
			return "", database.NewError(database.CodeInvalid, "cron workspace is invalid")
		}
	case ".db":
		if filepath.Base(absolute) != cronDatabaseFilename || filepath.Base(filepath.Dir(absolute)) != "cron" {
			return "", database.NewError(database.CodeInvalid, "cron workspace is invalid")
		}
		cronDir = filepath.Dir(absolute)
	case ".json":
		if filepath.Base(absolute) != cronLegacyFilename || filepath.Base(filepath.Dir(absolute)) != "cron" {
			return "", database.NewError(database.CodeInvalid, "cron workspace is invalid")
		}
		cronDir = filepath.Dir(absolute)
	default:
		return "", database.NewError(database.CodeInvalid, "cron workspace is invalid")
	}
	return canonicalCronWorkspace(filepath.Dir(cronDir))
}

func canonicalCronWorkspace(workspace string) (string, error) {
	if workspace == "" || workspace != strings.TrimSpace(workspace) || strings.ContainsRune(workspace, 0) {
		return "", database.NewError(database.CodeInvalid, "cron workspace is invalid")
	}
	absolute, err := filepath.Abs(filepath.Clean(workspace))
	if err != nil {
		return "", database.NewError(database.CodeInvalid, "cron workspace is invalid")
	}
	probe := absolute
	var suffix []string
	for {
		if _, statErr := os.Lstat(probe); statErr == nil {
			resolved, resolveErr := filepath.EvalSymlinks(probe)
			if resolveErr != nil {
				return "", database.NewError(database.CodeInvalid, "cron workspace is invalid")
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
			return "", database.NewError(database.CodeInvalid, "cron workspace is invalid")
		}
	}
}

func cronWorkspaceSelector(workspace string) (string, error) {
	canonical, err := canonicalCronWorkspace(workspace)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(filepath.Clean(canonical)))
	return fmt.Sprintf("%x", digest[:8]), nil
}

func validCronWorkspaceSelector(selector string) bool {
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

func cronRequestStoreID(request database.Request) (database.StoreID, error) {
	var header struct {
		StoreID database.StoreID `json:"store_id"`
	}
	if json.Unmarshal(request.Payload, &header) != nil || !header.StoreID.Valid() {
		return "", invalidCronRequest()
	}
	return header.StoreID, nil
}

func (cs *CronService) callBroker(
	ctx context.Context,
	operation string,
	input,
	output any,
	mutation bool,
) error {
	if cs == nil || cs.brokerClient == nil {
		return database.NewError(database.CodeUnavailable, "cron broker client is unavailable")
	}
	if cs.brokerErr != nil {
		return cs.brokerErr
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if mutation {
		return cs.brokerClient.CallWithOptions(
			ctx, BrokerDomain, BrokerVersion, operation, input, output,
			database.CallOptions{Mutation: true},
		)
	}
	return cs.brokerClient.Call(ctx, BrokerDomain, BrokerVersion, operation, input, output)
}

func (cs *CronService) acceptBrokerResponse(response cronBrokerResponse) error {
	if cs == nil || cs.brokerClient == nil {
		return database.NewError(database.CodeUnavailable, "cron broker client is unavailable")
	}
	cs.initErr = nil
	return nil
}

func validCronStoreID(storeID database.StoreID) bool {
	return storeID.Valid()
}

func validCronJobID(value string) bool {
	return value != "" && len(value) <= maximumCronIDBytes && utf8.ValidString(value) &&
		!strings.ContainsRune(value, 0)
}

func validateCronAddInput(input cronAddRequest) error {
	return validateCronJob(&CronJob{
		ID: "validation", Name: input.Name, Schedule: input.Schedule,
		Payload: CronPayload{
			Kind: "agent_turn", Message: input.Message, Channel: input.Channel, To: input.To,
		},
	})
}

func invalidCronRequest() error {
	return database.NewError(database.CodeInvalid, "cron request is invalid")
}

func mapCronBrokerError(err error) error {
	if err == nil {
		return nil
	}
	if code := database.CodeOf(err); code != database.CodeInternal {
		return database.NewError(code, "cron operation failed")
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return database.NewError(database.CodeDeadline, "cron operation deadline was exceeded")
	case errors.Is(err, sqlitestore.ErrTooNew):
		return database.NewError(database.CodeUnsupported, "cron store schema is newer than supported")
	case errors.Is(err, sqlitestore.ErrInvalidSchema), errors.Is(err, sqlitestore.ErrIntegrity):
		return database.NewError(database.CodeIntegrity, "cron store integrity validation failed")
	default:
		return database.NewError(database.CodeInternal, "cron operation failed")
	}
}

func isCronNotFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not found")
}

var _ database.Handler = (*BrokerHandler)(nil)
