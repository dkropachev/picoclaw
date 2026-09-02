package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/messageutil"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// registerSessionRoutes binds session list and detail endpoints to the ServeMux.
func (h *Handler) registerSessionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions", h.handleListSessions)
	mux.HandleFunc("GET /api/sessions/{id}", h.handleGetSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", h.handleDeleteSession)
}

// sessionFile is the unchanged HTTP projection of one SQLite-backed session.
type sessionFile struct {
	Key      string              `json:"key"`
	Messages []providers.Message `json:"messages"`
	Summary  string              `json:"summary,omitempty"`
	Created  time.Time           `json:"created"`
	Updated  time.Time           `json:"updated"`
}

// sessionListItem is a lightweight summary returned by GET /api/sessions.
type sessionListItem struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Preview      string `json:"preview"`
	MessageCount int    `json:"message_count"`
	Created      string `json:"created"`
	Updated      string `json:"updated"`
}

type sessionChatMessage struct {
	Role        string                  `json:"role"`
	Content     string                  `json:"content"`
	Kind        string                  `json:"kind,omitempty"`
	ModelName   string                  `json:"model_name,omitempty"`
	CreatedAt   *time.Time              `json:"created_at,omitempty"`
	Media       []string                `json:"media,omitempty"`
	Attachments []sessionChatAttachment `json:"attachments,omitempty"`
	ToolCalls   []utils.VisibleToolCall `json:"tool_calls,omitempty"`
}

type sessionChatAttachment struct {
	Type        string `json:"type,omitempty"`
	URL         string `json:"url,omitempty"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// legacyPicoSessionPrefix remains a logical-key compatibility alias.
const (
	legacyPicoSessionPrefix = "agent:main:pico:direct:pico:"
	picoSessionPrefix       = legacyPicoSessionPrefix

	maxSessionTitleRunes = 60

	handledToolResponseSummaryText = "Requested output delivered via tool attachment."
)

func defaultToolFeedbackMaxArgsLength() int {
	defaults := config.AgentDefaults{}
	return defaults.GetToolFeedbackMaxArgsLength()
}

// extractLegacyPicoSessionID extracts the session UUID from an old Pico key.
// Returns the UUID and true if the key matches the Pico session pattern.
func extractLegacyPicoSessionID(key string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(key), ":")
	if len(parts) < 5 || parts[0] != "agent" || strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	directIndex := -1
	for index := 2; index < len(parts); index++ {
		if parts[index] == "direct" {
			directIndex = index
			break
		}
	}
	if directIndex < 0 || directIndex == len(parts)-1 {
		return "", false
	}
	if directIndex > 2 && !strings.EqualFold(parts[2], "pico") {
		return "", false
	}
	peerID := strings.Join(parts[directIndex+1:], ":")
	if !strings.HasPrefix(strings.ToLower(peerID), "pico:") {
		return "", false
	}
	sessionID := strings.TrimSpace(peerID[len("pico:"):])
	if sessionID != "" {
		return sessionID, true
	}
	return "", false
}

func sanitizeSessionKey(key string) string {
	key = strings.ReplaceAll(key, ":", "_")
	key = strings.ReplaceAll(key, "/", "_")
	key = strings.ReplaceAll(key, "\\", "_")
	return key
}

func (h *Handler) readSQLiteSession(
	ctx context.Context,
	dir string,
	sessionKey string,
) (sessionFile, memory.SessionMeta, error) {
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		return sessionFile{}, memory.SessionMeta{}, err
	}
	defer store.Close()

	messages, meta, historyUpdatedAt, err := store.ReadSessionState(ctx, sessionKey)
	if err != nil {
		return sessionFile{}, memory.SessionMeta{}, err
	}
	// A row without any durable timestamp is not a readable launcher session.
	if historyUpdatedAt.IsZero() {
		return sessionFile{}, memory.SessionMeta{}, os.ErrNotExist
	}

	updated := meta.UpdatedAt
	created := meta.CreatedAt
	if created.IsZero() {
		created = historyUpdatedAt
	}
	if updated.IsZero() {
		updated = historyUpdatedAt
	}

	return sessionFile{
		Key:      meta.Key,
		Messages: messages,
		Summary:  meta.Summary,
		Created:  created,
		Updated:  updated,
	}, meta, nil
}

type picoSQLiteSessionRef struct {
	ID  string
	Key string
}

func extractPicoSessionIDFromScope(scope session.SessionScope) (string, bool) {
	if !strings.EqualFold(strings.TrimSpace(scope.Channel), "pico") {
		return "", false
	}

	candidates := []string{
		strings.TrimSpace(scope.Values["sender"]),
		strings.TrimSpace(scope.Values["chat"]),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if idx := strings.Index(candidate, "pico:"); idx >= 0 {
			sessionID := strings.TrimSpace(candidate[idx+len("pico:"):])
			if sessionID != "" {
				return sessionID, true
			}
		}
	}
	return "", false
}

func sessionRefFromMeta(meta memory.SessionMeta) (picoSQLiteSessionRef, bool) {
	if len(meta.Scope) == 0 {
		if sessionID, ok := extractLegacyPicoSessionID(meta.Key); ok {
			return picoSQLiteSessionRef{ID: sessionID, Key: meta.Key}, true
		}
		for _, alias := range meta.Aliases {
			if sessionID, ok := extractLegacyPicoSessionID(alias); ok {
				return picoSQLiteSessionRef{ID: sessionID, Key: meta.Key}, true
			}
		}
		return picoSQLiteSessionRef{}, false
	}
	var scope session.SessionScope
	if err := json.Unmarshal(meta.Scope, &scope); err != nil {
		return picoSQLiteSessionRef{}, false
	}
	// A structured non-Pico channel is authoritative. Its sender may happen to
	// look like a legacy Pico peer, but treating that value as a launcher ID
	// would expose (and allow deleting) another channel's history.
	channel := strings.TrimSpace(scope.Channel)
	if channel != "" && !strings.EqualFold(channel, "pico") {
		return picoSQLiteSessionRef{}, false
	}
	sessionID, ok := extractPicoSessionIDFromScope(scope)
	if !ok {
		if legacySessionID, ok := extractLegacyPicoSessionID(meta.Key); ok {
			return picoSQLiteSessionRef{ID: legacySessionID, Key: meta.Key}, true
		}
		for _, alias := range meta.Aliases {
			if legacySessionID, ok := extractLegacyPicoSessionID(alias); ok {
				return picoSQLiteSessionRef{ID: legacySessionID, Key: meta.Key}, true
			}
		}
		return picoSQLiteSessionRef{}, false
	}
	return picoSQLiteSessionRef{ID: sessionID, Key: meta.Key}, true
}

func sessionRefFromReadState(meta memory.SessionMeta) (picoSQLiteSessionRef, bool) {
	if ref, ok := sessionRefFromMeta(meta); ok {
		return ref, true
	}
	// A migrated metadata-less opaque history remains a launcher compatibility
	// projection. Its exact opaque key is also its HTTP ID.
	if len(meta.Scope) == 0 && len(meta.Aliases) == 0 && session.IsOpaqueSessionKey(meta.Key) {
		return picoSQLiteSessionRef{ID: meta.Key, Key: meta.Key}, true
	}
	return picoSQLiteSessionRef{}, false
}

func (h *Handler) findPicoSQLiteSessions(
	ctx context.Context,
	dir string,
) ([]picoSQLiteSessionRef, error) {
	store, err := memory.NewStore(dir)
	if err != nil {
		return nil, err
	}
	defer store.Close()

	refs := make([]picoSQLiteSessionRef, 0)
	for _, key := range store.ListSessions() {
		meta, err := store.GetSessionMeta(ctx, key)
		if err != nil {
			return nil, err
		}
		ref, ok := sessionRefFromReadState(meta)
		if !ok || ref.Key == "" || ref.ID == "" {
			continue
		}
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].ID != refs[j].ID {
			return refs[i].ID < refs[j].ID
		}
		return refs[i].Key < refs[j].Key
	})
	return refs, nil
}

func (h *Handler) findPicoSQLiteSession(
	ctx context.Context,
	dir,
	sessionID string,
) (picoSQLiteSessionRef, error) {
	refs, err := h.findPicoSQLiteSessions(ctx, dir)
	if err != nil {
		return picoSQLiteSessionRef{}, err
	}
	for _, ref := range refs {
		if ref.ID == sessionID {
			return ref, nil
		}
	}
	return picoSQLiteSessionRef{}, os.ErrNotExist
}

func (h *Handler) readPicoSQLiteSessionByID(
	ctx context.Context,
	dir string,
	sessionID string,
) (sessionFile, error) {
	refs, err := h.findPicoSQLiteSessions(ctx, dir)
	if err != nil {
		return sessionFile{}, err
	}
	var firstReadErr error
	for _, ref := range refs {
		if ref.ID != sessionID {
			continue
		}
		if h.sessionReadAfterLookup != nil {
			h.sessionReadAfterLookup()
		}
		sess, meta, readErr := h.readSQLiteSession(ctx, dir, ref.Key)
		if readErr != nil {
			if firstReadErr == nil {
				firstReadErr = readErr
			}
			continue
		}
		if isEmptySession(sess) {
			continue
		}
		currentRef, isCurrentPicoSession := sessionRefFromReadState(meta)
		if !isCurrentPicoSession || currentRef.Key != meta.Key || currentRef.ID != sessionID {
			continue
		}
		return sess, nil
	}
	if firstReadErr != nil {
		return sessionFile{}, firstReadErr
	}
	return sessionFile{}, os.ErrNotExist
}

func buildSessionListItem(sessionID string, sess sessionFile, toolFeedbackMaxArgsLength int) sessionListItem {
	transcript := visibleSessionMessages(sess.Messages, toolFeedbackMaxArgsLength)

	preview := ""
	for _, msg := range transcript {
		if msg.Role == "user" {
			preview = sessionChatMessagePreview(msg)
		}
		if preview != "" {
			break
		}
	}
	preview = truncateRunes(preview, maxSessionTitleRunes)

	if preview == "" {
		preview = truncateRunes(sess.Summary, maxSessionTitleRunes)
	}
	if preview == "" {
		preview = "(empty)"
	}
	title := preview

	return sessionListItem{
		ID:           sessionID,
		Title:        title,
		Preview:      preview,
		MessageCount: len(transcript),
		Created:      sess.Created.Format(time.RFC3339),
		Updated:      sess.Updated.Format(time.RFC3339),
	}
}

func isEmptySession(sess sessionFile) bool {
	return len(sess.Messages) == 0 && strings.TrimSpace(sess.Summary) == ""
}

func truncateRunes(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= maxLen {
		return string(runes)
	}
	return string(runes[:maxLen]) + "..."
}

func sessionChatMessageVisible(msg sessionChatMessage) bool {
	return strings.TrimSpace(msg.Content) != "" ||
		len(msg.Media) > 0 ||
		len(msg.Attachments) > 0 ||
		len(msg.ToolCalls) > 0
}

func sessionChatMessagePreview(msg sessionChatMessage) string {
	if content := strings.TrimSpace(msg.Content); content != "" {
		return content
	}
	if len(msg.Attachments) > 0 {
		if strings.EqualFold(strings.TrimSpace(msg.Attachments[0].Type), "image") {
			return "[image]"
		}
		return "[attachment]"
	}
	if len(msg.Media) > 0 {
		if strings.HasPrefix(strings.TrimSpace(msg.Media[0]), "data:image/") {
			return "[image]"
		}
		return "[attachment]"
	}
	if len(msg.ToolCalls) > 0 {
		return "[tool call]"
	}
	return ""
}

func visibleSessionMessages(messages []providers.Message, toolFeedbackMaxArgsLength int) []sessionChatMessage {
	return sessionTranscriptMessages(messages, toolFeedbackMaxArgsLength, false)
}

func detailSessionMessages(messages []providers.Message, toolFeedbackMaxArgsLength int) []sessionChatMessage {
	return sessionTranscriptMessages(messages, toolFeedbackMaxArgsLength, true)
}

func sessionTranscriptMessages(
	messages []providers.Message,
	toolFeedbackMaxArgsLength int,
	includeThoughts bool,
) []sessionChatMessage {
	transcript := make([]sessionChatMessage, 0, len(messages))

	for _, msg := range messages {
		attachments := sessionAttachments(msg)

		switch msg.Role {
		case "tool":
			continue

		case "user":
			chatMsg := sessionChatMessage{
				Role:        "user",
				Content:     msg.Content,
				ModelName:   msg.ModelName,
				CreatedAt:   msg.CreatedAt,
				Media:       append([]string(nil), msg.Media...),
				Attachments: attachments,
			}
			if sessionChatMessageVisible(chatMsg) {
				transcript = append(transcript, chatMsg)
			}

		case "assistant":
			if messageutil.IsTransientAssistantThoughtMessage(msg) {
				continue
			}
			if includeThoughts {
				if thoughtMsg, ok := assistantThoughtMessage(msg); ok {
					transcript = append(transcript, thoughtMsg)
				}
			}

			toolCallsMsg, hasToolCallsMsg := assistantToolCallsMessage(
				msg.ToolCalls,
				msg.ModelName,
				toolFeedbackMaxArgsLength,
				msg.CreatedAt,
			)
			visibleToolMessages := visibleAssistantToolMessages(msg.ToolCalls, msg.ModelName, msg.CreatedAt)

			// Pico web chat can persist both visible `message` tool output and a
			// later plain assistant reply in the same turn. Hide only the fixed
			// internal summary that marks handled tool delivery.
			content := msg.Content
			if assistantMessageInternalOnly(msg) {
				if len(attachments) == 0 {
					if hasToolCallsMsg {
						transcript = append(transcript, toolCallsMsg)
					}
					if len(visibleToolMessages) > 0 {
						transcript = append(transcript, visibleToolMessages...)
					}
					continue
				}
				content = ""
			}
			if hasToolCallsMsg && utils.ToolCallExplanationDuplicatesContent(content, msg.ToolCalls) {
				content = ""
			}

			chatMsg := sessionChatMessage{
				Role:        "assistant",
				Content:     content,
				ModelName:   msg.ModelName,
				CreatedAt:   msg.CreatedAt,
				Media:       append([]string(nil), msg.Media...),
				Attachments: attachments,
			}
			if !sessionChatMessageVisible(chatMsg) {
				if hasToolCallsMsg {
					transcript = append(transcript, toolCallsMsg)
				}
				if len(visibleToolMessages) > 0 {
					transcript = append(transcript, visibleToolMessages...)
				}
				continue
			}

			transcript = append(transcript, chatMsg)
			if hasToolCallsMsg {
				transcript = append(transcript, toolCallsMsg)
			}
			if len(visibleToolMessages) > 0 {
				transcript = append(transcript, visibleToolMessages...)
			}
		}
	}

	return filterSessionChatMessages(transcript)
}

func filterSessionChatMessages(messages []sessionChatMessage) []sessionChatMessage {
	filtered := messages[:0]
	for _, msg := range messages {
		if msg.Role != "user" && msg.Role != "assistant" {
			continue
		}
		filtered = append(filtered, msg)
	}
	return filtered
}

func sessionAttachments(msg providers.Message) []sessionChatAttachment {
	if len(msg.Attachments) == 0 {
		return nil
	}

	attachments := make([]sessionChatAttachment, 0, len(msg.Attachments))
	for _, attachment := range msg.Attachments {
		urlValue, ok := sessionAttachmentURL(attachment)
		if !ok {
			continue
		}
		attachmentType := strings.TrimSpace(attachment.Type)
		if attachmentType == "" {
			attachmentType = sessionAttachmentType(attachment)
		}
		attachments = append(attachments, sessionChatAttachment{
			Type:        attachmentType,
			URL:         urlValue,
			Filename:    strings.TrimSpace(attachment.Filename),
			ContentType: strings.TrimSpace(attachment.ContentType),
		})
	}

	if len(attachments) == 0 {
		return nil
	}
	return attachments
}

func sessionAttachmentURL(attachment providers.Attachment) (string, bool) {
	if rawURL := strings.TrimSpace(attachment.URL); rawURL != "" {
		return rawURL, true
	}

	ref := strings.TrimSpace(attachment.Ref)
	if ref == "" {
		return "", false
	}
	if strings.HasPrefix(ref, "media://") {
		// Persisted session history must only expose durable attachment locations.
		// media:// refs depend on the live in-memory MediaStore and may stop
		// resolving after a restart or cleanup, so omit them from reopened history.
		return "", false
	}
	return ref, true
}

func sessionAttachmentType(attachment providers.Attachment) string {
	contentType := strings.ToLower(strings.TrimSpace(attachment.ContentType))
	filename := strings.ToLower(strings.TrimSpace(attachment.Filename))
	rawRef := strings.ToLower(strings.TrimSpace(attachment.Ref))
	rawURL := strings.ToLower(strings.TrimSpace(attachment.URL))

	switch {
	case strings.HasPrefix(contentType, "image/"),
		strings.HasPrefix(rawRef, "data:image/"),
		strings.HasPrefix(rawURL, "data:image/"):
		return "image"
	case strings.HasPrefix(contentType, "audio/"):
		return "audio"
	case strings.HasPrefix(contentType, "video/"):
		return "video"
	}

	switch ext := filepath.Ext(filename); ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg":
		return "image"
	case ".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac", ".wma", ".opus":
		return "audio"
	case ".mp4", ".avi", ".mov", ".webm", ".mkv":
		return "video"
	default:
		return "file"
	}
}

func assistantMessageInternalOnly(msg providers.Message) bool {
	return strings.TrimSpace(msg.Content) == handledToolResponseSummaryText
}

func assistantThoughtMessage(msg providers.Message) (sessionChatMessage, bool) {
	reasoning := strings.TrimSpace(msg.ReasoningContent)
	if reasoning == "" {
		return sessionChatMessage{}, false
	}
	if reasoning == strings.TrimSpace(msg.Content) {
		return sessionChatMessage{}, false
	}
	return sessionChatMessage{
		Role:      "assistant",
		Content:   reasoning,
		Kind:      "thought",
		ModelName: msg.ModelName,
		CreatedAt: msg.CreatedAt,
	}, true
}

func assistantToolCallsMessage(
	toolCalls []providers.ToolCall,
	modelName string,
	toolFeedbackMaxArgsLength int,
	createdAt *time.Time,
) (sessionChatMessage, bool) {
	if len(toolCalls) == 0 {
		return sessionChatMessage{}, false
	}
	if toolFeedbackMaxArgsLength <= 0 {
		toolFeedbackMaxArgsLength = defaultToolFeedbackMaxArgsLength()
	}

	visibleToolCalls := utils.BuildVisibleToolCalls(toolCalls, toolFeedbackMaxArgsLength)
	if len(visibleToolCalls) == 0 {
		return sessionChatMessage{}, false
	}

	return sessionChatMessage{
		Role:      "assistant",
		Kind:      "tool_calls",
		ModelName: modelName,
		CreatedAt: createdAt,
		ToolCalls: visibleToolCalls,
	}, true
}

func visibleAssistantToolArgsPreview(
	tc providers.ToolCall,
	toolFeedbackMaxArgsLength int,
) string {
	return utils.VisibleToolCallArgumentsPreview(tc, toolFeedbackMaxArgsLength)
}

func visibleAssistantToolMessages(
	toolCalls []providers.ToolCall,
	modelName string,
	createdAt *time.Time,
) []sessionChatMessage {
	if len(toolCalls) == 0 {
		return nil
	}

	messages := make([]sessionChatMessage, 0, len(toolCalls))
	for _, tc := range toolCalls {
		name, argsJSON := utils.VisibleToolCallNameAndArguments(tc)
		if name != "message" {
			continue
		}
		content, ok := parseMessageToolContent(argsJSON)
		if !ok {
			continue
		}
		messages = append(messages, sessionChatMessage{
			Role:      "assistant",
			Content:   content,
			ModelName: modelName,
			CreatedAt: createdAt,
		})
	}

	return messages
}

func parseMessageToolContent(argsJSON string) (string, bool) {
	var args struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", false
	}
	if strings.TrimSpace(args.Content) == "" {
		return "", false
	}
	return args.Content, true
}

// sessionsDir resolves the path to the gateway's session storage directory.
// It reads the workspace from config, falling back to ~/.picoclaw/workspace.
func (h *Handler) sessionsDir() (string, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return "", err
	}

	return resolveSessionsDir(cfg.Agents.Defaults.Workspace), nil
}

func (h *Handler) sessionRuntimeSettings() (string, int, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return "", 0, err
	}

	return resolveSessionsDir(cfg.Agents.Defaults.Workspace), cfg.Agents.Defaults.GetToolFeedbackMaxArgsLength(), nil
}

func resolveSessionsDir(workspace string) string {
	if workspace == "" {
		home, _ := os.UserHomeDir()
		workspace = filepath.Join(home, ".picoclaw", "workspace")
	}

	// Expand ~ prefix
	if len(workspace) > 0 && workspace[0] == '~' {
		home, _ := os.UserHomeDir()
		if len(workspace) > 1 && workspace[1] == '/' {
			workspace = home + workspace[1:]
		} else {
			workspace = home
		}
	}

	return filepath.Join(workspace, "sessions")
}

// handleListSessions returns a list of Pico session summaries.
//
//	GET /api/sessions
func (h *Handler) handleListSessions(w http.ResponseWriter, r *http.Request) {
	dir, toolFeedbackMaxArgsLength, err := h.sessionRuntimeSettings()
	if err != nil {
		http.Error(w, "failed to resolve sessions directory", http.StatusInternalServerError)
		return
	}

	if _, err := os.ReadDir(dir); err != nil {
		// Directory doesn't exist yet = no sessions
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]sessionListItem{})
		return
	}

	items := []sessionListItem{}
	seen := make(map[string]struct{})

	refs, findErr := h.findPicoSQLiteSessions(r.Context(), dir)
	if findErr != nil {
		http.Error(w, "failed to load sessions", http.StatusInternalServerError)
		return
	}
	if h.sessionReadAfterLookup != nil {
		h.sessionReadAfterLookup()
	}
	for _, ref := range refs {
		if _, exists := seen[ref.ID]; exists {
			continue
		}
		sess, meta, loadErr := h.readSQLiteSession(r.Context(), dir, ref.Key)
		if loadErr != nil || isEmptySession(sess) {
			continue
		}
		currentRef, isCurrentPicoSession := sessionRefFromReadState(meta)
		if !isCurrentPicoSession || currentRef.Key != meta.Key || currentRef.ID != ref.ID {
			continue
		}
		seen[ref.ID] = struct{}{}
		items = append(items, buildSessionListItem(ref.ID, sess, toolFeedbackMaxArgsLength))
	}

	// Sort by updated descending (most recent first)
	sort.Slice(items, func(i, j int) bool {
		return items[i].Updated > items[j].Updated
	})

	// Pagination parameters
	offsetStr := r.URL.Query().Get("offset")
	limitStr := r.URL.Query().Get("limit")

	offset := 0
	limit := 20 // Default limit

	if val, err := strconv.Atoi(offsetStr); err == nil && val >= 0 {
		offset = val
	}
	if val, err := strconv.Atoi(limitStr); err == nil && val > 0 {
		limit = val
	}

	totalItems := len(items)

	end := offset + limit
	if offset >= totalItems {
		items = []sessionListItem{} // Out of bounds, return empty
	} else {
		if end > totalItems {
			end = totalItems
		}
		items = items[offset:end]
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

// handleGetSession returns the full message history for a specific session.
//
//	GET /api/sessions/{id}
func (h *Handler) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}

	dir, toolFeedbackMaxArgsLength, err := h.sessionRuntimeSettings()
	if err != nil {
		http.Error(w, "failed to resolve sessions directory", http.StatusInternalServerError)
		return
	}

	sess, err := h.readPicoSQLiteSessionByID(r.Context(), dir, sessionID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "session not found", http.StatusNotFound)
		} else {
			http.Error(w, "failed to parse session", http.StatusInternalServerError)
		}
		return
	}

	for i := range sess.Messages {
		if sess.Messages[i].CreatedAt == nil {
			sess.Messages[i].CreatedAt = &sess.Updated
		}
	}
	messages := detailSessionMessages(sess.Messages, toolFeedbackMaxArgsLength)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"id":       sessionID,
		"messages": messages,
		"summary":  sess.Summary,
		"created":  sess.Created.Format(time.RFC3339),
		"updated":  sess.Updated.Format(time.RFC3339),
	})
}

// handleDeleteSession deletes a specific session.
//
//	DELETE /api/sessions/{id}
func (h *Handler) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}

	dir, err := h.sessionsDir()
	if err != nil {
		http.Error(w, "failed to resolve sessions directory", http.StatusInternalServerError)
		return
	}

	removed := false
	if deleted, deleteErr := h.deletePicoSQLiteSessions(r.Context(), dir, sessionID); deleteErr != nil {
		http.Error(w, "failed to delete session", http.StatusInternalServerError)
		return
	} else {
		removed = removed || deleted
	}

	if !removed {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deletePicoSQLiteSessions(
	ctx context.Context,
	dir string,
	sessionID string,
) (bool, error) {
	refs, err := h.findPicoSQLiteSessions(ctx, dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	candidateKeys := make([]string, 0)
	for _, ref := range refs {
		if ref.ID == sessionID {
			candidateKeys = append(candidateKeys, ref.Key)
		}
	}
	if len(candidateKeys) == 0 {
		return false, nil
	}
	if h.sessionDeleteAfterLookup != nil {
		h.sessionDeleteAfterLookup()
	}
	refs, err = h.findPicoSQLiteSessions(ctx, dir)
	if err != nil {
		return false, err
	}
	candidateKeys = candidateKeys[:0]
	for _, ref := range refs {
		if ref.ID == sessionID {
			candidateKeys = append(candidateKeys, ref.Key)
		}
	}
	if len(candidateKeys) == 0 {
		return false, nil
	}

	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		return false, err
	}
	deleted, deleteErr := store.DeleteSessionsWithAliasesMatching(
		ctx,
		candidateKeys,
		func(meta memory.SessionMeta, metadataFound bool) bool {
			if !metadataFound {
				if orphanID, isLegacyPico := extractLegacyPicoSessionID(meta.Key); isLegacyPico {
					return orphanID == sessionID
				}
				return session.IsOpaqueSessionKey(meta.Key) && meta.Key == sessionID
			}
			currentRef, isCurrentPicoSession := sessionRefFromReadState(meta)
			return isCurrentPicoSession && currentRef.Key == meta.Key && currentRef.ID == sessionID
		},
		func(_ memory.SessionMeta, alias string) bool {
			aliasID, isOwnedPicoAlias := extractLegacyPicoSessionID(alias)
			return isOwnedPicoAlias && aliasID == sessionID
		},
	)
	closeErr := store.Close()
	if deleteErr != nil {
		return deleted, deleteErr
	}
	return deleted, closeErr
}
