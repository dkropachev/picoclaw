package api

import (
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/routing"
)

const (
	reviewAttentionAgentsPath           = "/api/reviews/attention-agents"
	reviewAttentionAgentPageSize        = 256
	reviewAttentionAgentsQueryMaxBytes  = 64
	reviewAttentionAgentIfMatchMaxBytes = 4 << 10
	reviewAttentionAgentsInvalidRequest = "invalid_attention_agents_request"
	reviewAttentionAgentsUnavailable    = "attention_agents_unavailable"
	implicitMainAgentName               = "Main"
)

type reviewAttentionAgentIdentity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type reviewAttentionAgentsResponse struct {
	Agents         []reviewAttentionAgentIdentity `json:"agents"`
	DefaultAgentID string                         `json:"default_agent_id"`
	ConfigRevision string                         `json:"config_revision"`
	NextCursor     string                         `json:"next_cursor,omitempty"`
}

// handleGetReviewAttentionAgents returns one bounded identity-only page from
// the agent catalog. If-Match and cursor together fence every page to the
// exact public/security config generation supplied by the policy response.
func (h *Handler) handleGetReviewAttentionAgents(
	w http.ResponseWriter,
	r *http.Request,
) {
	setReviewResponseHeaders(w)
	if r == nil || r.Method != http.MethodGet {
		h.handleReviewAttentionAgentsMethodNotAllowed(w, r)
		return
	}
	if !canonicalReviewRequestPath(r) ||
		r.URL.Path != reviewAttentionAgentsPath ||
		r.URL.ForceQuery {
		writeReviewAPIError(
			w,
			http.StatusBadRequest,
			reviewAttentionAgentsInvalidRequest,
		)
		return
	}

	offset, err := parseReviewAttentionAgentCursor(r.URL.RawQuery)
	if err != nil {
		writeReviewAPIError(
			w,
			http.StatusBadRequest,
			reviewAttentionAgentsInvalidRequest,
		)
		return
	}
	expectedRevision, err := parseReviewAttentionAgentIfMatch(r.Header)
	if err != nil {
		writeReviewAPIError(
			w,
			http.StatusBadRequest,
			reviewAttentionAgentsInvalidRequest,
		)
		return
	}

	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()

	cfg, revision, err := config.LoadCurrentConfigForUpdateSnapshotIfRevision(
		h.configPath,
		expectedRevision,
	)
	if errors.Is(err, config.ErrConfigRevisionMismatch) {
		writeReviewAPIError(
			w,
			http.StatusConflict,
			reviewAttentionConfigRevisionMismatch,
		)
		return
	}
	if err != nil || len(revision) == 0 ||
		len(revision) > reviewAttentionAgentIfMatchMaxBytes {
		writeReviewAPIError(
			w,
			http.StatusInternalServerError,
			reviewAttentionAgentsUnavailable,
		)
		return
	}
	if validateAgentConfiguration(cfg) != nil {
		writeReviewAPIError(
			w,
			http.StatusInternalServerError,
			reviewAttentionAgentsUnavailable,
		)
		return
	}

	identities, err := reviewAttentionAgentIdentities(cfg)
	if err != nil {
		writeReviewAPIError(
			w,
			http.StatusInternalServerError,
			reviewAttentionAgentsUnavailable,
		)
		return
	}
	if uint64(len(identities)) > math.MaxUint32 {
		writeReviewAPIError(
			w,
			http.StatusInternalServerError,
			reviewAttentionAgentsUnavailable,
		)
		return
	}
	if uint64(offset) >= uint64(len(identities)) {
		writeReviewAPIError(
			w,
			http.StatusBadRequest,
			reviewAttentionAgentsInvalidRequest,
		)
		return
	}

	start := int(offset)
	end := len(identities)
	if len(identities)-start > reviewAttentionAgentPageSize {
		end = start + reviewAttentionAgentPageSize
	}
	response := reviewAttentionAgentsResponse{
		Agents:         identities[start:end],
		DefaultAgentID: effectiveDefaultAgentID(cfg),
		ConfigRevision: revision,
	}
	if end < len(identities) {
		response.NextCursor = strconv.FormatUint(uint64(end), 10)
	}
	writeReviewJSON(w, http.StatusOK, response)
}

func (h *Handler) handleReviewAttentionAgentsMethodNotAllowed(
	w http.ResponseWriter,
	_ *http.Request,
) {
	w.Header().Set("Allow", http.MethodGet)
	writeReviewAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func parseReviewAttentionAgentCursor(rawQuery string) (uint32, error) {
	if rawQuery == "" {
		return 0, nil
	}
	if len(rawQuery) > reviewAttentionAgentsQueryMaxBytes ||
		!strings.HasPrefix(rawQuery, "cursor=") {
		return 0, errors.New("invalid attention-agent cursor")
	}
	rawCursor := strings.TrimPrefix(rawQuery, "cursor=")
	parsed, err := strconv.ParseUint(rawCursor, 10, 32)
	if err != nil || parsed == 0 ||
		parsed%reviewAttentionAgentPageSize != 0 ||
		strconv.FormatUint(parsed, 10) != rawCursor {
		return 0, errors.New("invalid attention-agent cursor")
	}
	return uint32(parsed), nil
}

func parseReviewAttentionAgentIfMatch(header http.Header) (string, error) {
	values := reviewHeaderValues(header, "If-Match")
	if len(values) != 1 {
		return "", errors.New("exactly one If-Match header is required")
	}
	value := values[0]
	if len(value) < 3 || len(value) > reviewAttentionAgentIfMatchMaxBytes ||
		value[0] != '"' || value[len(value)-1] != '"' {
		return "", errors.New("a strong quoted If-Match value is required")
	}
	opaque := value[1 : len(value)-1]
	for index := 0; index < len(opaque); index++ {
		character := opaque[index]
		if character == '"' || character == ',' || character < 0x21 ||
			character == 0x7f {
			return "", errors.New("invalid If-Match opaque value")
		}
	}
	return opaque, nil
}

func reviewAttentionAgentIdentities(
	cfg *config.Config,
) ([]reviewAttentionAgentIdentity, error) {
	if cfg == nil {
		return nil, errors.New("agent configuration is required")
	}
	if len(cfg.Agents.List) == 0 {
		return []reviewAttentionAgentIdentity{{
			ID:   routing.DefaultAgentID,
			Name: implicitMainAgentName,
		}}, nil
	}

	identities := make(
		[]reviewAttentionAgentIdentity,
		0,
		len(cfg.Agents.List),
	)
	for index := range cfg.Agents.List {
		agent := &cfg.Agents.List[index]
		name, err := normalizeAgentScalar(agent.Name, 256)
		if err != nil {
			return nil, err
		}
		identities = append(identities, reviewAttentionAgentIdentity{
			ID:   agent.ID,
			Name: name,
		})
	}
	sort.Slice(identities, func(left, right int) bool {
		return identities[left].ID < identities[right].ID
	})
	return identities, nil
}
