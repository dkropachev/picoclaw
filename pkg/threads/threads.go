package threads

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/messageutil"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
)

const (
	TypeGeneral       = "general"
	TypeCoding        = "coding"
	TypeReviewing     = "reviewing"
	TypeInvestigating = "investigating"

	DefaultLimit = 50
	MaxLimit     = 200

	legacyPicoSessionPrefix = "agent:main:pico:direct:pico:"
)

var (
	absolutePathRE = regexp.MustCompile(`(^|[\s'"(])(/[^\s'",)]+)`)
	repoRE         = regexp.MustCompile(`(?i)\b((?:git@|https?://)[^\s'",)]+(?:\.git)?)`)
	branchRE       = regexp.MustCompile(`(?i)\bbranch[:\s]+([A-Za-z0-9._/\-]+)`)
	prRE           = regexp.MustCompile(`(?i)\b(?:pr|pull request)[:\s#]+([0-9]+)`)
	contextTokenRE = regexp.MustCompile(`(?i)\b([a-z][a-z0-9_-]*)\s*:\s*([^\s]+)`)
)

type Store struct {
	Dir string
}

type Thread struct {
	ID           string            `json:"id"`
	SessionKey   string            `json:"session_key,omitempty"`
	Title        string            `json:"title"`
	Preview      string            `json:"preview"`
	Type         string            `json:"type"`
	Context      map[string]string `json:"context,omitempty"`
	MessageCount int               `json:"message_count"`
	Created      time.Time         `json:"created"`
	Updated      time.Time         `json:"updated"`
	SourceQuery  string            `json:"source_query,omitempty"`
	Score        int               `json:"score,omitempty"`
}

type SearchOptions struct {
	Query   string
	Type    string
	Context map[string]string
	Offset  int
	Limit   int
}

type CreateRequest struct {
	ID          string
	Type        string
	Title       string
	Context     map[string]string
	SourceQuery string
}

type PicoAllocation struct {
	SessionID string
	Key       string
	Scope     session.SessionScope
	Aliases   []string
	AgentID   string
}

type metaFile struct {
	meta memory.SessionMeta
	base string
}

func ResolveSessionsDir(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		home, _ := os.UserHomeDir()
		workspace = filepath.Join(home, ".picoclaw", "workspace")
	}
	if strings.HasPrefix(workspace, "~/") {
		home, _ := os.UserHomeDir()
		workspace = filepath.Join(home, workspace[2:])
	} else if workspace == "~" {
		home, _ := os.UserHomeDir()
		workspace = home
	}
	return filepath.Join(workspace, "sessions")
}

func NewStoreFromWorkspace(workspace string) Store {
	return Store{Dir: ResolveSessionsDir(workspace)}
}

func (s Store) Search(opts SearchOptions) ([]Thread, error) {
	items, err := s.List()
	if err != nil {
		return nil, err
	}

	parsedType, parsedContext := ParseContextFilters(opts.Query)
	if strings.TrimSpace(opts.Type) == "" {
		opts.Type = parsedType
	}
	if opts.Context == nil {
		opts.Context = map[string]string{}
	}
	for key, value := range parsedContext {
		if _, exists := opts.Context[key]; !exists {
			opts.Context[key] = value
		}
	}

	query := strings.TrimSpace(opts.Query)
	results := make([]Thread, 0, len(items))
	for _, item := range items {
		if !matchesType(item, opts.Type) || !matchesContext(item, opts.Context) {
			continue
		}
		score := scoreThread(item, query)
		if query != "" && score == 0 {
			continue
		}
		item.Score = score
		results = append(results, item)
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Updated.After(results[j].Updated)
	})

	return paginate(results, opts.Offset, opts.Limit), nil
}

func (s Store) List() ([]Thread, error) {
	if s.Dir == "" {
		return nil, errors.New("threads: sessions directory is empty")
	}
	entries, err := os.ReadDir(s.Dir)
	if os.IsNotExist(err) {
		return []Thread{}, nil
	}
	if err != nil {
		return nil, err
	}

	metas := make(map[string]metaFile)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		path := filepath.Join(s.Dir, entry.Name())
		meta, err := readMeta(path, "")
		if err != nil {
			continue
		}
		base := strings.TrimSuffix(entry.Name(), ".meta.json")
		metas[base] = metaFile{meta: meta, base: base}
	}

	items := make([]Thread, 0, len(metas))
	seen := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		base := strings.TrimSuffix(entry.Name(), ".jsonl")
		mf, ok := metas[base]
		if !ok {
			key, id, validSessionFile := sessionKeyAndIDFromJSONLFilename(entry.Name())
			if !validSessionFile {
				continue
			}
			mf = metaFile{meta: memory.SessionMeta{Key: key}, base: base}
			if _, exists := seen[id]; exists {
				continue
			}
		}
		thread, ok := s.threadFromMetaFile(mf)
		if !ok {
			continue
		}
		seen[thread.ID] = struct{}{}
		items = append(items, thread)
	}

	for base, mf := range metas {
		if _, ok := seen[base]; ok {
			continue
		}
		thread, ok := s.threadFromMetaFile(mf)
		if !ok {
			continue
		}
		if _, exists := seen[thread.ID]; exists {
			continue
		}
		seen[thread.ID] = struct{}{}
		items = append(items, thread)
	}

	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Updated.After(items[j].Updated)
	})
	return items, nil
}

func (s Store) CreatePicoThread(ctx context.Context, cfg *config.Config, req CreateRequest) (Thread, error) {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	if strings.TrimSpace(s.Dir) == "" {
		s.Dir = ResolveSessionsDir(cfg.Agents.Defaults.Workspace)
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Thread{}, err
	}

	allocation := AllocatePicoThread(cfg, req.ID)
	if allocation.SessionID == "" || allocation.Key == "" {
		return Thread{}, errors.New("threads: failed to allocate pico thread")
	}

	base := filepath.Join(s.Dir, sanitizeSessionKey(allocation.Key))
	jsonlPath := base + ".jsonl"
	if f, err := os.OpenFile(jsonlPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
		return Thread{}, err
	} else if closeErr := f.Close(); closeErr != nil {
		return Thread{}, closeErr
	}

	rawScope, err := json.Marshal(allocation.Scope)
	if err != nil {
		return Thread{}, err
	}

	now := time.Now()
	meta, err := readMeta(base+".meta.json", allocation.Key)
	if err != nil {
		return Thread{}, err
	}
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.UpdatedAt = now
	meta.Key = allocation.Key
	meta.Scope = rawScope
	meta.Aliases = normalizeAliases(allocation.Key, allocation.Aliases)
	meta.ThreadType = NormalizeType(firstNonEmpty(req.Type, InferType(req.Title+" "+req.SourceQuery)))
	meta.ThreadTitle = truncateRunes(firstNonEmpty(req.Title, req.SourceQuery, "New thread"), 80)
	meta.ThreadContext = MergeContext(ExtractContext(req.SourceQuery+" "+req.Title), req.Context)
	meta.ThreadSourceQuery = strings.TrimSpace(req.SourceQuery)
	if strings.TrimSpace(meta.Summary) == "" {
		meta.Summary = meta.ThreadTitle
	}

	if err := writeMeta(base+".meta.json", meta); err != nil {
		return Thread{}, err
	}

	thread, ok := s.threadFromMetaFile(metaFile{meta: meta, base: sanitizeSessionKey(allocation.Key)})
	if !ok {
		return Thread{}, errors.New("threads: created thread could not be loaded")
	}
	_ = ctx
	return thread, nil
}

func (s Store) Get(id string) (Thread, bool, error) {
	items, err := s.List()
	if err != nil {
		return Thread{}, false, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, true, nil
		}
	}
	return Thread{}, false, nil
}

func AllocatePicoThread(cfg *config.Config, requestedID string) PicoAllocation {
	sessionID := strings.TrimSpace(requestedID)
	if sessionID == "" {
		sessionID = GenerateSessionID()
	}
	inbound := bus.InboundContext{
		Channel:  "pico",
		ChatID:   "pico:" + sessionID,
		ChatType: "direct",
		SenderID: "pico-user",
		Raw: map[string]string{
			"platform":   "pico",
			"session_id": sessionID,
		},
	}
	route := routing.NewRouteResolver(cfg).ResolveRoute(inbound)
	allocation := session.AllocateRouteSession(session.AllocationInput{
		AgentID:       route.AgentID,
		Context:       inbound,
		SessionPolicy: route.SessionPolicy,
	})
	return PicoAllocation{
		SessionID: sessionID,
		Key:       allocation.SessionKey,
		Scope:     allocation.Scope,
		Aliases:   allocation.SessionAliases,
		AgentID:   route.AgentID,
	}
}

func GenerateSessionID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("session-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("session-%d-%s", time.Now().UnixMilli(), hex.EncodeToString(b[:]))
}

func NormalizeType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case TypeCoding, "code", "implementation", "implementing":
		return TypeCoding
	case TypeReviewing, "review", "pr", "pull_request":
		return TypeReviewing
	case TypeInvestigating, "investigate", "debugging", "debug":
		return TypeInvestigating
	default:
		return TypeGeneral
	}
}

func InferType(text string) string {
	normalized := strings.ToLower(text)
	switch {
	case strings.Contains(normalized, "review") ||
		strings.Contains(normalized, "pull request") ||
		strings.Contains(normalized, " pr "):
		return TypeReviewing
	case strings.Contains(normalized, "investigate") ||
		strings.Contains(normalized, "debug") ||
		strings.Contains(normalized, "find why") ||
		strings.Contains(normalized, "root cause"):
		return TypeInvestigating
	case strings.Contains(normalized, "code") ||
		strings.Contains(normalized, "implement") ||
		strings.Contains(normalized, "fix") ||
		strings.Contains(normalized, "repo") ||
		absolutePathRE.MatchString(text):
		return TypeCoding
	default:
		return TypeGeneral
	}
}

func ExtractContext(text string) map[string]string {
	context := map[string]string{}
	for _, match := range contextTokenRE.FindAllStringSubmatch(text, -1) {
		if len(match) == 3 {
			key := strings.ToLower(strings.TrimSpace(match[1]))
			value := strings.Trim(strings.TrimSpace(match[2]), ".,;")
			if key != "" && value != "" && key != "type" && !skipContextKey(key) {
				context[key] = value
			}
		}
	}
	if match := repoRE.FindStringSubmatch(text); len(match) > 1 {
		context["repo"] = strings.Trim(match[1], ".,;")
	}
	if match := absolutePathRE.FindStringSubmatch(text); len(match) > 2 {
		context["location"] = strings.Trim(match[2], ".,;")
	}
	if match := branchRE.FindStringSubmatch(text); len(match) > 1 {
		context["branch"] = strings.Trim(match[1], ".,;")
	}
	if match := prRE.FindStringSubmatch(text); len(match) > 1 {
		context["pr"] = strings.Trim(match[1], ".,;#")
	}
	return cleanContext(context)
}

func ParseContextFilters(query string) (string, map[string]string) {
	threadType := ""
	context := map[string]string{}
	for _, match := range contextTokenRE.FindAllStringSubmatch(query, -1) {
		if len(match) != 3 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(match[1]))
		value := strings.Trim(strings.TrimSpace(match[2]), ".,;")
		if key == "type" {
			threadType = NormalizeType(value)
			continue
		}
		if key != "" && value != "" && !skipContextKey(key) {
			context[key] = value
		}
	}
	return threadType, cleanContext(context)
}

func skipContextKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "http", "https", "com", "git":
		return true
	default:
		return false
	}
}

func MergeContext(base map[string]string, overlays ...map[string]string) map[string]string {
	merged := map[string]string{}
	for key, value := range base {
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			merged[key] = value
		}
	}
	for _, overlay := range overlays {
		for key, value := range overlay {
			key = strings.ToLower(strings.TrimSpace(key))
			value = strings.TrimSpace(value)
			if key != "" && value != "" {
				merged[key] = value
			}
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func (s Store) threadFromMetaFile(mf metaFile) (Thread, bool) {
	meta := mf.meta
	key := strings.TrimSpace(meta.Key)
	if key == "" {
		key = sessionKeyFromSanitizedBase(mf.base)
	}
	id, ok := picoSessionIDFromMeta(meta)
	if !ok || id == "" {
		if fallbackKey, fallbackID, fallbackOK := sessionKeyAndIDFromJSONLFilename(mf.base + ".jsonl"); fallbackOK {
			key = fallbackKey
			id = fallbackID
			ok = true
		}
	}
	if !ok || id == "" {
		return Thread{}, false
	}

	messages, _ := readMessages(filepath.Join(s.Dir, sanitizeSessionKey(key)+".jsonl"), meta.Skip)
	visible := visibleMessages(messages)
	title, preview := titleAndPreview(meta, visible)
	created := meta.CreatedAt
	updated := meta.UpdatedAt
	if created.IsZero() || updated.IsZero() {
		if info, err := os.Stat(filepath.Join(s.Dir, sanitizeSessionKey(key)+".jsonl")); err == nil {
			if created.IsZero() {
				created = info.ModTime()
			}
			if updated.IsZero() {
				updated = info.ModTime()
			}
		}
	}
	if created.IsZero() {
		created = updated
	}
	if updated.IsZero() {
		updated = created
	}
	if created.IsZero() && updated.IsZero() {
		return Thread{}, false
	}

	context := MergeContext(scopeContext(meta.Scope), meta.ThreadContext)
	threadType := NormalizeType(meta.ThreadType)
	if threadType == TypeGeneral {
		threadType = NormalizeType(InferType(title + " " + preview + " " + contextText(context)))
	}

	return Thread{
		ID:           id,
		SessionKey:   key,
		Title:        title,
		Preview:      preview,
		Type:         threadType,
		Context:      context,
		MessageCount: len(visible),
		Created:      created,
		Updated:      updated,
		SourceQuery:  strings.TrimSpace(meta.ThreadSourceQuery),
	}, true
}

func titleAndPreview(meta memory.SessionMeta, messages []providers.Message) (string, string) {
	title := strings.TrimSpace(meta.ThreadTitle)
	preview := ""
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}
		preview = messagePreview(msg)
		if preview != "" {
			break
		}
	}
	if preview == "" {
		preview = strings.TrimSpace(meta.Summary)
	}
	if preview == "" && title != "" {
		preview = title
	}
	if title == "" {
		title = preview
	}
	if title == "" {
		title = "New thread"
	}
	if preview == "" {
		preview = "(empty)"
	}
	return truncateRunes(title, 80), truncateRunes(preview, 120)
}

func visibleMessages(messages []providers.Message) []providers.Message {
	visible := make([]providers.Message, 0, len(messages))
	for _, msg := range messages {
		if messageutil.IsTransientAssistantThoughtMessage(msg) || msg.Role == "tool" {
			continue
		}
		if strings.TrimSpace(msg.Content) == "" &&
			len(msg.Media) == 0 &&
			len(msg.Attachments) == 0 &&
			len(msg.ToolCalls) == 0 {
			continue
		}
		visible = append(visible, msg)
	}
	return visible
}

func messagePreview(msg providers.Message) string {
	if content := strings.TrimSpace(msg.Content); content != "" {
		return content
	}
	if len(msg.Attachments) > 0 || len(msg.Media) > 0 {
		return "[attachment]"
	}
	if len(msg.ToolCalls) > 0 {
		return "[tool call]"
	}
	return ""
}

func scoreThread(thread Thread, query string) int {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return 1
	}
	haystack := strings.ToLower(strings.Join([]string{
		thread.ID,
		thread.Title,
		thread.Preview,
		thread.Type,
		contextText(thread.Context),
		thread.SourceQuery,
	}, " "))
	score := 0
	if strings.Contains(haystack, query) {
		score += 8
	}
	for _, token := range strings.Fields(query) {
		token = strings.Trim(token, ".,;")
		if token == "" || strings.Contains(token, ":") {
			continue
		}
		if strings.Contains(haystack, token) {
			score += 2
		}
	}
	_, parsedContext := ParseContextFilters(query)
	for key, value := range parsedContext {
		if contextValueMatches(thread.Context[key], value) {
			score += 5
		}
	}
	return score
}

func matchesType(thread Thread, typ string) bool {
	typ = strings.TrimSpace(typ)
	return typ == "" || NormalizeType(thread.Type) == NormalizeType(typ)
}

func matchesContext(thread Thread, filter map[string]string) bool {
	if len(filter) == 0 {
		return true
	}
	for key, value := range filter {
		if !contextValueMatches(thread.Context[strings.ToLower(strings.TrimSpace(key))], value) {
			return false
		}
	}
	return true
}

func contextValueMatches(got, want string) bool {
	got = strings.ToLower(strings.TrimSpace(got))
	want = strings.ToLower(strings.TrimSpace(want))
	return want == "" || got == want || strings.Contains(got, want)
}

func paginate(items []Thread, offset, limit int) []Thread {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	if offset >= len(items) {
		return []Thread{}
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

func picoSessionIDFromMeta(meta memory.SessionMeta) (string, bool) {
	if len(meta.Scope) > 0 {
		var scope session.SessionScope
		if err := json.Unmarshal(meta.Scope, &scope); err == nil {
			if id, ok := picoSessionIDFromScope(scope); ok {
				return id, true
			}
		}
	}
	if id, ok := legacyPicoID(meta.Key); ok {
		return id, true
	}
	for _, alias := range meta.Aliases {
		if id, ok := legacyPicoID(alias); ok {
			return id, true
		}
	}
	return "", false
}

func picoSessionIDFromScope(scope session.SessionScope) (string, bool) {
	if !strings.EqualFold(strings.TrimSpace(scope.Channel), "pico") {
		return "", false
	}
	for _, key := range []string{"sender", "chat"} {
		value := strings.TrimSpace(scope.Values[key])
		if value == "" {
			continue
		}
		if idx := strings.Index(value, "pico:"); idx >= 0 {
			id := strings.TrimSpace(value[idx+len("pico:"):])
			if id != "" {
				return id, true
			}
		}
	}
	return "", false
}

func scopeContext(raw json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var scope session.SessionScope
	if err := json.Unmarshal(raw, &scope); err != nil {
		return nil
	}
	context := map[string]string{
		"agent":   strings.TrimSpace(scope.AgentID),
		"channel": strings.TrimSpace(scope.Channel),
	}
	if account := strings.TrimSpace(scope.Account); account != "" {
		context["account"] = account
	}
	for _, dimension := range scope.Dimensions {
		dimension = strings.TrimSpace(dimension)
		value := strings.TrimSpace(scope.Values[dimension])
		if dimension != "" && value != "" {
			context[dimension] = value
		}
	}
	return cleanContext(context)
}

func contextText(context map[string]string) string {
	if len(context) == 0 {
		return ""
	}
	keys := make([]string, 0, len(context))
	for key := range context {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+":"+context[key])
	}
	return strings.Join(parts, " ")
}

func readMessages(path string, skip int) ([]providers.Message, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return []providers.Message{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	messages := make([]providers.Message, 0)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	seen := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		seen++
		if seen <= skip {
			continue
		}
		var msg providers.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		messages = append(messages, msg)
	}
	return messages, scanner.Err()
}

func readMeta(path, fallbackKey string) (memory.SessionMeta, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return memory.SessionMeta{Key: fallbackKey}, nil
	}
	if err != nil {
		return memory.SessionMeta{}, err
	}
	var meta memory.SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return memory.SessionMeta{}, err
	}
	if meta.Key == "" {
		meta.Key = fallbackKey
	}
	return meta, nil
}

func writeMeta(path string, meta memory.SessionMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(path, data, 0o644)
}

func sessionKeyAndIDFromJSONLFilename(name string) (string, string, bool) {
	if !strings.HasSuffix(name, ".jsonl") {
		return "", "", false
	}
	base := strings.TrimSuffix(name, ".jsonl")
	if base == "" {
		return "", "", false
	}
	legacyPrefix := sanitizeSessionKey(legacyPicoSessionPrefix)
	if strings.HasPrefix(base, legacyPrefix) {
		id := strings.TrimPrefix(base, legacyPrefix)
		if id != "" {
			return legacyPicoSessionPrefix + id, id, true
		}
	}
	if session.IsOpaqueSessionKey(base) {
		return base, base, true
	}
	return "", "", false
}

func sessionKeyFromSanitizedBase(base string) string {
	if session.IsOpaqueSessionKey(base) {
		return base
	}
	return strings.ReplaceAll(base, "_", ":")
}

func legacyPicoID(key string) (string, bool) {
	if strings.HasPrefix(key, legacyPicoSessionPrefix) {
		id := strings.TrimPrefix(key, legacyPicoSessionPrefix)
		return id, id != ""
	}
	return "", false
}

func sanitizeSessionKey(key string) string {
	key = strings.ReplaceAll(key, ":", "_")
	key = strings.ReplaceAll(key, "/", "_")
	key = strings.ReplaceAll(key, "\\", "_")
	return key
}

func normalizeAliases(canonicalKey string, aliases []string) []string {
	out := make([]string, 0, len(aliases))
	seen := map[string]struct{}{}
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || alias == canonicalKey {
			continue
		}
		if _, ok := seen[alias]; ok {
			continue
		}
		seen[alias] = struct{}{}
		out = append(out, alias)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cleanContext(context map[string]string) map[string]string {
	if len(context) == 0 {
		return nil
	}
	cleaned := map[string]string{}
	for key, value := range context {
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.Trim(strings.TrimSpace(value), ".,;")
		if key != "" && value != "" {
			cleaned[key] = value
		}
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

func truncateRunes(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func ParsePositiveInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}
