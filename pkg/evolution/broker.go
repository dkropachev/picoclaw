package evolution

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	dbcatalog "github.com/sipeed/picoclaw/pkg/database/catalog"
)

const (
	BrokerDomain  = "evolution"
	BrokerVersion = 1
	// BrokerPreflightOperation opens and fully validates exactly one cataloged
	// evolution store without mutating domain data.
	BrokerPreflightOperation = "preflight"

	evolutionOpAppendRecords = "append-records"
	evolutionOpSaveRecords   = "save-records"
	evolutionOpLoadRecords   = "load-records"
	evolutionOpMarkClustered = "mark-clustered"
	evolutionOpMergePatterns = "merge-patterns"
	evolutionOpSaveDrafts    = "save-drafts"
	evolutionOpLoadDrafts    = "load-drafts"
	evolutionOpSaveProfile   = "save-profile"
	evolutionOpLoadProfile   = "load-profile"
	evolutionOpLoadProfiles  = "load-profiles"

	evolutionBrokerPageSize = 200
)

type evolutionBrokerRequest struct {
	StoreID        database.StoreID `json:"store_id"`
	Class          string           `json:"class,omitempty"`
	Records        []LearningRecord `json:"records,omitempty"`
	IDs            []string         `json:"ids,omitempty"`
	Drafts         []SkillDraft     `json:"drafts,omitempty"`
	Profile        SkillProfile     `json:"profile,omitempty"`
	WorkspaceID    string           `json:"workspace_id,omitempty"`
	SkillName      string           `json:"skill_name,omitempty"`
	ExpectedDigest string           `json:"expected_digest,omitempty"`
	ExpectedExists bool             `json:"expected_exists,omitempty"`
	CAS            bool             `json:"cas,omitempty"`
	Offset         int              `json:"offset,omitempty"`
}

type evolutionBrokerTargetRequest struct {
	StoreID database.StoreID `json:"store_id"`
}

type evolutionBrokerResponse struct {
	Ready    bool             `json:"ready,omitempty"`
	Updated  bool             `json:"updated,omitempty"`
	Records  []LearningRecord `json:"records,omitempty"`
	Drafts   []SkillDraft     `json:"drafts,omitempty"`
	Profile  SkillProfile     `json:"profile,omitempty"`
	Profiles []SkillProfile   `json:"profiles,omitempty"`
	Exists   bool             `json:"exists,omitempty"`
	Digest   string           `json:"digest,omitempty"`
	Fallback bool             `json:"fallback,omitempty"`
	More     bool             `json:"more,omitempty"`
}

type evolutionBrokerTarget struct {
	id    database.StoreID
	paths Paths
}

func canonicalEvolutionPath(value string) (string, error) {
	if strings.TrimSpace(value) == "" || strings.ContainsRune(value, 0) {
		return "", errors.New("evolution path is invalid")
	}
	return filepath.Abs(filepath.Clean(value))
}

func canonicalEvolutionDatabase(paths Paths) (string, error) {
	paths = normalizedEvolutionPaths(paths)
	databasePath := paths.Database
	if !filepath.IsAbs(databasePath) {
		databasePath = filepath.Join(paths.Workspace, databasePath)
	}
	return canonicalEvolutionPath(databasePath)
}

func resolveEvolutionWorkspace(home, configured string) (string, error) {
	path := strings.TrimSpace(configured)
	if strings.HasPrefix(path, "~/") || path == "~" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			path = userHome
		} else {
			path = filepath.Join(userHome, path[2:])
		}
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(home, path)
	}
	return canonicalEvolutionPath(path)
}

func configuredEvolutionTargets(home string, cfg *config.Config) (map[database.StoreID]evolutionBrokerTarget, error) {
	catalog, err := dbcatalog.New(home, cfg)
	if err != nil {
		return nil, err
	}
	result := make(map[database.StoreID]evolutionBrokerTarget)
	add := func(id database.StoreID, paths Paths) error {
		if !catalog.Contains(id) {
			return errors.New("evolution store is absent from catalog")
		}
		paths = normalizedEvolutionPaths(paths)
		db, pathErr := canonicalEvolutionDatabase(paths)
		if pathErr != nil {
			return pathErr
		}
		paths.Database = db
		result[id] = evolutionBrokerTarget{id: id, paths: paths}
		return nil
	}
	primary, err := canonicalEvolutionPath(cfg.WorkspacePath())
	if err != nil {
		return nil, err
	}
	primaryRoot := strings.TrimSpace(cfg.Evolution.StateDir)
	if primaryRoot != "" && !filepath.IsAbs(primaryRoot) {
		primaryRoot = filepath.Join(primary, primaryRoot)
	}
	if err := add("workspace.evolution", NewPaths(primary, primaryRoot)); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{primary: {}}
	for _, agent := range cfg.Agents.List {
		raw := strings.TrimSpace(agent.Workspace)
		if raw == "" {
			continue
		}
		workspace, err := resolveEvolutionWorkspace(home, raw)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[workspace]; ok {
			continue
		}
		seen[workspace] = struct{}{}
		digest := sha256.Sum256([]byte(filepath.Clean(workspace)))
		id, err := database.ParseStoreID("workspace." + hex.EncodeToString(digest[:8]) + ".evolution")
		if err != nil {
			return nil, err
		}
		if err := add(id, NewPaths(workspace, "")); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func resolveEvolutionBrokerStoreID(paths Paths) (database.StoreID, error) {
	home, err := database.CanonicalHome(config.GetHome())
	if err != nil {
		return "", err
	}
	configPath := strings.TrimSpace(os.Getenv(config.EnvConfig))
	if configPath == "" {
		configPath = filepath.Join(home, "config.json")
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return "", database.NewError(database.CodeUnavailable, "evolution broker configuration is unavailable")
	}
	targets, err := configuredEvolutionTargets(home, cfg)
	if err != nil {
		return "", database.NewError(database.CodeUnavailable, "evolution broker catalog is unavailable")
	}
	databasePath, err := canonicalEvolutionDatabase(paths)
	if err != nil {
		return "", database.NewError(database.CodeInvalid, "evolution store path is invalid")
	}
	workspace, err := canonicalEvolutionPath(paths.Workspace)
	if err != nil {
		return "", database.NewError(database.CodeInvalid, "evolution workspace is invalid")
	}
	for id, target := range targets {
		targetWorkspace, _ := canonicalEvolutionPath(target.paths.Workspace)
		if target.paths.Database == databasePath && targetWorkspace == workspace {
			return id, nil
		}
	}
	return "", database.NewError(database.CodeUnsupported, "evolution store is not broker-cataloged")
}

func (s *Store) usesEvolutionBroker() bool {
	return s != nil && (s.broker != nil || s.brokerErr != nil)
}

func (s *Store) brokerCall(op string, in evolutionBrokerRequest, out *evolutionBrokerResponse, mutation bool) error {
	if s == nil || s.broker == nil {
		if s != nil && s.brokerErr != nil {
			return s.brokerErr
		}
		return errors.New("evolution broker unavailable")
	}
	in.StoreID = s.storeID
	var err error
	if mutation {
		err = s.broker.CallWithOptions(
			context.Background(),
			BrokerDomain,
			BrokerVersion,
			op,
			in,
			out,
			database.CallOptions{Mutation: true},
		)
	} else {
		err = s.broker.Call(context.Background(), BrokerDomain, BrokerVersion, op, in, out)
	}
	return decodeEvolutionBrokerError(err)
}

func (s *Store) brokerRecords(ctx context.Context, op, class string, records []LearningRecord) error {
	var out evolutionBrokerResponse
	in := evolutionBrokerRequest{Class: class, Records: records}
	if s == nil || s.broker == nil {
		return s.brokerCall(op, in, &out, true)
	}
	in.StoreID = s.storeID
	err := s.broker.CallWithOptions(
		ctx,
		BrokerDomain,
		BrokerVersion,
		op,
		in,
		&out,
		database.CallOptions{Mutation: true},
	)
	return decodeEvolutionBrokerError(err)
}

func (s *Store) brokerIDs(op string, ids []string) error {
	var out evolutionBrokerResponse
	return s.brokerCall(op, evolutionBrokerRequest{IDs: ids}, &out, true)
}

func (s *Store) brokerDrafts(op string, drafts []SkillDraft) error {
	var out evolutionBrokerResponse
	return s.brokerCall(op, evolutionBrokerRequest{Drafts: drafts}, &out, true)
}

func (s *Store) brokerSaveProfile(profile SkillProfile, digest string, cas bool) error {
	var out evolutionBrokerResponse
	return s.brokerCall(
		evolutionOpSaveProfile,
		evolutionBrokerRequest{Profile: profile, ExpectedDigest: digest, ExpectedExists: cas, CAS: cas},
		&out,
		true,
	)
}

func (s *Store) brokerLoadRecords(class string) ([]LearningRecord, error) {
	var result []LearningRecord
	for offset := 0; ; offset += evolutionBrokerPageSize {
		var out evolutionBrokerResponse
		err := s.brokerCall(evolutionOpLoadRecords, evolutionBrokerRequest{Class: class, Offset: offset}, &out, false)
		if err != nil {
			return nil, err
		}
		result = append(result, out.Records...)
		if !out.More {
			return result, nil
		}
	}
}

func (s *Store) brokerLoadDrafts() ([]SkillDraft, error) {
	var result []SkillDraft
	for offset := 0; ; offset += evolutionBrokerPageSize {
		var out evolutionBrokerResponse
		err := s.brokerCall(evolutionOpLoadDrafts, evolutionBrokerRequest{Offset: offset}, &out, false)
		if err != nil {
			return nil, err
		}
		result = append(result, out.Drafts...)
		if !out.More {
			return result, nil
		}
	}
}

func (s *Store) brokerLoadProfiles() ([]SkillProfile, error) {
	var result []SkillProfile
	for offset := 0; ; offset += evolutionBrokerPageSize {
		var out evolutionBrokerResponse
		err := s.brokerCall(evolutionOpLoadProfiles, evolutionBrokerRequest{Offset: offset}, &out, false)
		if err != nil {
			return nil, err
		}
		result = append(result, out.Profiles...)
		if !out.More {
			return result, nil
		}
	}
}

func (s *Store) brokerProfile(workspace, skill string) (evolutionBrokerResponse, error) {
	var out evolutionBrokerResponse
	err := s.brokerCall(
		evolutionOpLoadProfile,
		evolutionBrokerRequest{WorkspaceID: workspace, SkillName: skill},
		&out,
		false,
	)
	return out, err
}

func (s *Store) brokerLoadProfile(skill string) (SkillProfile, error) {
	out, err := s.brokerProfile(s.paths.Workspace, skill)
	if err != nil {
		return SkillProfile{}, err
	}
	if !out.Exists {
		return SkillProfile{}, os.ErrNotExist
	}
	return out.Profile, nil
}

func (s *Store) brokerUpdateProfile(workspace, skill string, update func(*SkillProfile, bool) error) error {
	if update == nil {
		return errors.New("evolution profile update is nil")
	}
	out, err := s.brokerProfile(workspace, skill)
	if err != nil {
		return err
	}
	profile := out.Profile
	if err := update(&profile, out.Exists); err != nil {
		return err
	}
	if !out.Exists && isZeroSkillProfile(profile) {
		return nil
	}
	var response evolutionBrokerResponse
	return s.brokerCall(
		evolutionOpSaveProfile,
		evolutionBrokerRequest{
			Profile:        profile,
			WorkspaceID:    workspace,
			SkillName:      skill,
			ExpectedDigest: out.Digest,
			ExpectedExists: out.Exists,
			CAS:            true,
		},
		&response,
		true,
	)
}

func evolutionProfileDigest(profile SkillProfile, exists bool) (string, error) {
	if !exists {
		return "absent", nil
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func paginateEvolution[T any](values []T, offset int) ([]T, bool) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(values) {
		offset = len(values)
	}
	end := offset + evolutionBrokerPageSize
	if end > len(values) {
		end = len(values)
	}
	return values[offset:end], end < len(values)
}

type evolutionBrokerStore struct {
	paths Paths
	once  sync.Once
	store *Store
	err   error
}

func (target *evolutionBrokerStore) open() (*Store, error) {
	if target == nil {
		return nil, database.NewError(database.CodeUnavailable, "evolution store unavailable")
	}
	target.once.Do(func() {
		target.store = newLocalStore(target.paths)
		target.err = target.store.retain()
		if target.err != nil {
			_ = target.store.Close()
			target.store = nil
		}
	})
	return target.store, target.err
}

func (target *evolutionBrokerStore) close() error {
	if target == nil || target.store == nil {
		return nil
	}
	return target.store.Close()
}

type BrokerHandler struct {
	mu     sync.RWMutex
	stores map[database.StoreID]*evolutionBrokerStore
	closed bool
}

func NewBrokerHandler(home string, cfg *config.Config) (*BrokerHandler, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() &&
		!allowUnfencedEvolutionProviderForTests.Load() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"evolution broker handler requires authenticated broker authority",
		)
	}
	if cfg == nil {
		return nil, database.NewError(database.CodeInvalid, "evolution broker configuration invalid")
	}
	targets, err := configuredEvolutionTargets(home, cfg)
	if err != nil {
		return nil, err
	}
	handler := &BrokerHandler{stores: make(map[database.StoreID]*evolutionBrokerStore, len(targets))}
	for id, target := range targets {
		handler.stores[id] = &evolutionBrokerStore{paths: target.paths}
	}
	return handler, nil
}

func (h *BrokerHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if h == nil || request.Domain != BrokerDomain || request.Version != BrokerVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain unsupported")
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return nil, database.NewError(database.CodeUnavailable, "evolution broker unavailable")
	}
	if request.Operation == BrokerPreflightOperation {
		var in evolutionBrokerTargetRequest
		if request.DecodePayload(&in) != nil || !in.StoreID.Valid() {
			return nil, database.NewError(database.CodeInvalid, "evolution request invalid")
		}
		target := h.stores[in.StoreID]
		if target == nil {
			return nil, database.NewError(database.CodeUnauthorized, "evolution store not cataloged")
		}
		if _, err := target.open(); err != nil {
			return nil, mapEvolutionBrokerError(err)
		}
		return evolutionBrokerResponse{Ready: true}, nil
	}
	var in evolutionBrokerRequest
	if err := request.DecodePayload(&in); err != nil {
		return nil, database.NewError(database.CodeInvalid, "evolution request invalid")
	}
	target := h.stores[in.StoreID]
	if target == nil {
		return nil, database.NewError(database.CodeUnauthorized, "evolution store not cataloged")
	}
	store, err := target.open()
	if err != nil {
		return nil, mapEvolutionBrokerError(err)
	}
	out, err := h.dispatch(ctx, store, request.Operation, in)
	return out, mapEvolutionBrokerError(err)
}

func (h *BrokerHandler) dispatch(
	ctx context.Context,
	s *Store,
	op string,
	in evolutionBrokerRequest,
) (evolutionBrokerResponse, error) {
	switch op {
	case evolutionOpAppendRecords:
		return evolutionBrokerResponse{Updated: true}, s.appendRecordsLocal(ctx, in.Class, in.Records)
	case evolutionOpSaveRecords:
		return evolutionBrokerResponse{Updated: true}, s.saveRecords(ctx, in.Class, in.Records)
	case evolutionOpLoadRecords:
		values, err := s.loadRecords(ctx, in.Class)
		page, more := paginateEvolution(values, in.Offset)
		return evolutionBrokerResponse{Records: page, More: more}, err
	case evolutionOpMarkClustered:
		return evolutionBrokerResponse{Updated: true}, s.markTaskRecordsClusteredLocal(in.IDs)
	case evolutionOpMergePatterns:
		return evolutionBrokerResponse{Updated: true}, s.mergePatternRecordsLocal(in.Records)
	case evolutionOpSaveDrafts:
		return evolutionBrokerResponse{Updated: true}, s.saveDraftsLocal(in.Drafts)
	case evolutionOpLoadDrafts:
		values, err := s.loadDraftsLocal()
		page, more := paginateEvolution(values, in.Offset)
		return evolutionBrokerResponse{Drafts: page, More: more}, err
	case evolutionOpSaveProfile:
		return evolutionBrokerResponse{Updated: true}, h.saveProfile(ctx, s, in)
	case evolutionOpLoadProfile:
		profile, exists, fallback, err := loadProfileLocalDetailed(s, in.WorkspaceID, in.SkillName)
		digest, digestErr := evolutionProfileDigest(profile, exists)
		if err == nil {
			err = digestErr
		}
		return evolutionBrokerResponse{Profile: profile, Exists: exists, Digest: digest, Fallback: fallback}, err
	case evolutionOpLoadProfiles:
		values, err := s.loadProfilesLocal()
		page, more := paginateEvolution(values, in.Offset)
		return evolutionBrokerResponse{Profiles: page, More: more}, err
	default:
		return evolutionBrokerResponse{}, database.NewError(database.CodeUnsupported, "evolution operation unsupported")
	}
}

func (s *Store) appendRecordsLocal(ctx context.Context, class string, records []LearningRecord) error {
	if class == "" {
		return s.AppendTaskOrPatternRecords(ctx, records)
	}
	return s.appendRecords(ctx, class, records)
}

func (s *Store) markTaskRecordsClusteredLocal(ids []string) error {
	broker := s.broker
	s.broker = nil
	defer func() { s.broker = broker }()
	return s.MarkTaskRecordsClustered(ids)
}

func (s *Store) mergePatternRecordsLocal(records []LearningRecord) error {
	broker := s.broker
	s.broker = nil
	defer func() { s.broker = broker }()
	return s.MergePatternRecords(records)
}

func (s *Store) saveDraftsLocal(drafts []SkillDraft) error {
	broker := s.broker
	s.broker = nil
	defer func() { s.broker = broker }()
	return s.SaveDrafts(drafts)
}

func (s *Store) loadDraftsLocal() ([]SkillDraft, error) {
	broker := s.broker
	s.broker = nil
	defer func() { s.broker = broker }()
	return s.LoadDrafts()
}

func (s *Store) loadProfilesLocal() ([]SkillProfile, error) {
	broker := s.broker
	s.broker = nil
	defer func() { s.broker = broker }()
	return s.LoadProfiles()
}

func loadProfileLocalDetailed(s *Store, workspace, skill string) (SkillProfile, bool, bool, error) {
	if err := validateEvolutionSkillName(skill); err != nil {
		return SkillProfile{}, false, false, err
	}
	db, err := s.open(context.Background())
	if err != nil {
		return SkillProfile{}, false, false, err
	}
	return loadProfileQueryDetailed(context.Background(), db, s.paths, workspace, skill)
}

func loadProfileQueryDetailed(
	ctx context.Context,
	query evolutionQueryer,
	paths Paths,
	workspace, skill string,
) (SkillProfile, bool, bool, error) {
	profile, found, err := loadEvolutionProfile(ctx, query, strings.TrimSpace(workspace), skill)
	if err != nil {
		return SkillProfile{}, false, false, err
	}
	fallback := false
	if !found && workspace != "" && usesDefaultWorkspaceState(paths, workspace) {
		profile, found, err = loadEvolutionProfile(ctx, query, "", skill)
		fallback = found
	}
	return profile, found, fallback, err
}

func (h *BrokerHandler) saveProfile(ctx context.Context, s *Store, in evolutionBrokerRequest) error {
	return s.immediate(ctx, func(conn *sql.Conn) error {
		if in.CAS {
			current, exists, fallback, err := loadProfileQueryDetailed(ctx, conn, s.paths, in.WorkspaceID, in.SkillName)
			if err != nil {
				return err
			}
			digest, err := evolutionProfileDigest(current, exists)
			if err != nil {
				return err
			}
			if exists != in.ExpectedExists || digest != in.ExpectedDigest {
				return errors.New("evolution profile changed during update")
			}
			if fallback {
				in.Profile.WorkspaceID = strings.TrimSpace(in.WorkspaceID)
				in.Profile.SkillName = in.SkillName
			}
		}
		if err := validateEvolutionProfile(in.Profile); err != nil {
			return err
		}
		_, err := putEvolutionProfile(ctx, conn, in.Profile, false)
		return err
	})
}

func mapEvolutionBrokerError(err error) error {
	if err == nil {
		return nil
	}
	if code := database.CodeOf(err); code != database.CodeInternal {
		return database.NewError(code, "evolution operation failed")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return database.NewError(database.CodeDeadline, "evolution deadline exceeded")
	}
	if errors.Is(err, os.ErrNotExist) {
		return database.NewError(database.CodeNotFound, "evolution_not_found")
	}
	if strings.Contains(err.Error(), "changed during update") {
		return database.NewError(database.CodeConflict, "evolution_conflict")
	}
	return database.NewError(database.CodeInternal, "evolution operation failed")
}

func decodeEvolutionBrokerError(err error) error {
	if err == nil {
		return nil
	}
	if database.CodeOf(err) == database.CodeOutcomeUnknown {
		return err
	}
	var value *database.Error
	if errors.As(err, &value) {
		if value.Message == "evolution_not_found" {
			return os.ErrNotExist
		}
		if value.Message == "evolution_conflict" {
			return errors.New("evolution profile changed during update")
		}
		if value.Code == database.CodeDeadline {
			return context.DeadlineExceeded
		}
	}
	return err
}

func (h *BrokerHandler) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.closed = true
	var result error
	for _, store := range h.stores {
		result = errors.Join(result, store.close())
	}
	return result
}

var _ database.Handler = (*BrokerHandler)(nil)
