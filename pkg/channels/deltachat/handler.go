package deltachat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/utils"
)

const (
	deltaChatRetryInitial = 250 * time.Millisecond
	deltaChatRetryMaximum = 5 * time.Second
	deltaChatDownloadDone = "Done"
)

var (
	errDeltaChatDownloadPending    = errors.New("Delta Chat message download is pending")
	errDeltaChatAttachmentTooLarge = errors.New("Delta Chat attachment exceeds media size limit")
	errDeltaChatAttachmentCopy     = errors.New("Delta Chat attachment copy failed")
)

type deltaChatProcessState uint8

const (
	deltaChatProcessStopped deltaChatProcessState = iota
	deltaChatProcessComplete
	deltaChatProcessDownloadPending
)

type deltaChatDownloadPendingError struct {
	messageID     int64
	chatID        int64
	rfc724MID     string
	downloadState string
	requestIssued bool
}

func (err *deltaChatDownloadPendingError) Error() string {
	return fmt.Sprintf(
		"%s: message %d is in state %q",
		errDeltaChatDownloadPending,
		err.messageID,
		err.downloadState,
	)
}

func (err *deltaChatDownloadPendingError) Unwrap() error {
	return errDeltaChatDownloadPending
}

type deltaChatPendingDownload struct {
	messageID         int64
	chatID            int64
	rfc724MID         string
	downloadRequested bool
}

type deltaChatListenerState struct {
	pendingDownloads map[int64]*deltaChatPendingDownload
}

func newDeltaChatListenerState() *deltaChatListenerState {
	return &deltaChatListenerState{
		pendingDownloads: make(map[int64]*deltaChatPendingDownload),
	}
}

func (state *deltaChatListenerState) oldestPending() *deltaChatPendingDownload {
	if state == nil || len(state.pendingDownloads) == 0 {
		return nil
	}
	var oldest *deltaChatPendingDownload
	for _, pending := range state.pendingDownloads {
		if pending == nil {
			continue
		}
		if oldest == nil || pending.messageID < oldest.messageID {
			oldest = pending
		}
	}
	return oldest
}

func (state *deltaChatListenerState) recordPending(
	pendingErr *deltaChatDownloadPendingError,
) *deltaChatPendingDownload {
	if state == nil || pendingErr == nil {
		return nil
	}
	pending := state.pendingDownloads[pendingErr.messageID]
	if pending == nil {
		pending = &deltaChatPendingDownload{
			messageID: pendingErr.messageID,
			chatID:    pendingErr.chatID,
		}
		state.pendingDownloads[pendingErr.messageID] = pending
	}
	if pending.chatID <= 0 {
		pending.chatID = pendingErr.chatID
	}
	if pending.rfc724MID == "" {
		pending.rfc724MID = strings.TrimSpace(pendingErr.rfc724MID)
	}
	if pendingErr.requestIssued || pendingErr.downloadState == "InProgress" {
		pending.downloadRequested = true
	}
	return pending
}

// listen treats provider events only as notifications that the ordered
// high-water queue may have changed. Every message is selected through
// get_next_msgs, so stale startup events cannot replay already acknowledged
// messages and an overflow can recover work that lost its individual event.
func (c *DeltaChatChannel) listen() {
	logger.InfoCF("deltachat", "Listening for messages", map[string]any{
		"account_id": c.accountID,
		"email":      c.selfAddr,
	})
	listenerState := newDeltaChatListenerState()
	if c.drainStartupBacklog(listenerState) == deltaChatProcessStopped {
		return
	}

	for c.IsRunning() && c.ctx.Err() == nil {
		raw, err := c.rpc.call(c.ctx, "get_next_event")
		if err != nil {
			if c.ctx.Err() != nil || !c.IsRunning() {
				return
			}
			logger.ErrorCF("deltachat", "get_next_event failed", map[string]any{"error": err.Error()})
			if !c.waitForRetry(time.Second) {
				return
			}
			continue
		}

		var event dcEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			continue
		}
		if c.processListenerEvent(event, listenerState) == deltaChatProcessStopped {
			return
		}
	}
}

func (c *DeltaChatChannel) processListenerEvent(
	event dcEvent,
	listenerState *deltaChatListenerState,
) deltaChatProcessState {
	// Delta Chat emits overflow with contextId=0 because the event belongs to
	// the account manager rather than one account. Handle it before filtering.
	if event.Event.Kind == "EventChannelOverflow" {
		logger.WarnCF("deltachat", "Provider event channel overflowed; draining ordered queue", nil)
		return c.drainStartupBacklog(listenerState)
	}
	if event.ContextID != c.accountID {
		return deltaChatProcessComplete
	}

	switch event.Event.Kind {
	case "IncomingMsg":
		return c.drainStartupBacklog(listenerState)
	case "MsgsChanged":
		// In bot mode Delta Chat deliberately reports a newly received
		// Pre-Message only as a message-specific MsgsChanged event. Wake the
		// ordered queue for that first notification as well as for later
		// download transitions. Generic chat-wide changes remain irrelevant.
		if event.Event.MsgID > 0 || len(listenerState.pendingDownloads) > 0 {
			return c.drainStartupBacklog(listenerState)
		}
	}
	return deltaChatProcessComplete
}

// drainStartupBacklog is used at startup and after provider notifications. It
// processes only IDs returned by the ordered provider queue. A failed or
// incomplete lower ID prevents acknowledgement from advancing past it.
func (c *DeltaChatChannel) drainStartupBacklog(
	listenerState *deltaChatListenerState,
) deltaChatProcessState {
	backoff := deltaChatRetryInitial
	for c.IsRunning() && c.ctx.Err() == nil {
		raw, err := c.rpc.call(c.ctx, "get_next_msgs", c.accountID)
		if err != nil {
			logger.ErrorCF("deltachat", "get_next_msgs ordered queue failed", map[string]any{
				"error": err.Error(),
			})
			if !c.waitForRetry(backoff) {
				return deltaChatProcessStopped
			}
			backoff = nextDeltaChatBackoff(backoff)
			continue
		}

		var messageIDs []int64
		if err := json.Unmarshal(raw, &messageIDs); err != nil {
			logger.WarnCF("deltachat", "Invalid get_next_msgs response", map[string]any{
				"error": err.Error(),
			})
			if !c.waitForRetry(backoff) {
				return deltaChatProcessStopped
			}
			backoff = nextDeltaChatBackoff(backoff)
			continue
		}
		if len(messageIDs) == 0 {
			if len(listenerState.pendingDownloads) > 0 {
				return deltaChatProcessDownloadPending
			}
			return deltaChatProcessComplete
		}
		sort.Slice(messageIDs, func(i, j int) bool { return messageIDs[i] < messageIDs[j] })
		logger.DebugCF("deltachat", "Draining message backlog", map[string]any{
			"count": len(messageIDs),
		})

		state, processErr := c.processBacklogBatch(
			messageIDs,
			listenerState,
		)
		if processErr != nil {
			logger.WarnCF("deltachat", "Ordered message batch remains retryable", map[string]any{
				"error": processErr.Error(),
			})
			if !c.waitForRetry(backoff) {
				return deltaChatProcessStopped
			}
			backoff = nextDeltaChatBackoff(backoff)
			continue
		}
		if state != deltaChatProcessComplete {
			return state
		}
		backoff = deltaChatRetryInitial
	}
	return deltaChatProcessStopped
}

func (c *DeltaChatChannel) processBacklogBatch(
	messageIDs []int64,
	listenerState *deltaChatListenerState,
) (deltaChatProcessState, error) {
	for index := 0; index < len(messageIDs); {
		pending := listenerState.oldestPending()
		boundary := len(messageIDs) - 1
		retirePendingID := int64(0)

		if pending != nil {
			pendingIndex := -1
			for candidateIndex := index; candidateIndex < len(messageIDs); candidateIndex++ {
				if messageIDs[candidateIndex] == pending.messageID {
					pendingIndex = candidateIndex
					break
				}
			}
			if pendingIndex >= 0 {
				boundary = pendingIndex
			} else {
				replacementIndex, found, err := c.correlatedReplacementBoundary(
					messageIDs[index:],
					pending.rfc724MID,
				)
				if err != nil {
					return deltaChatProcessComplete, err
				}
				if !found {
					// The batch may contain unrelated complete messages. Do not
					// let one retire the missing original or advance the
					// high-water cursor past it.
					return deltaChatProcessDownloadPending, nil
				}
				boundary = index + replacementIndex
				retirePendingID = pending.messageID
			}
		}

		for index <= boundary {
			state := c.processMessageWithRetry(messageIDs[index], listenerState)
			if state != deltaChatProcessComplete {
				return state, nil
			}
			index++
		}
		if retirePendingID > 0 {
			delete(listenerState.pendingDownloads, retirePendingID)
		}
	}
	if len(listenerState.pendingDownloads) > 0 {
		return deltaChatProcessDownloadPending, nil
	}
	return deltaChatProcessComplete, nil
}

// correlatedReplacementBoundary returns the last candidate in the batch whose
// RFC 724 Message-ID matches the incomplete original. Looking through the last
// match supports Delta Chat replacing one original with multiple ordered IDs.
func (c *DeltaChatChannel) correlatedReplacementBoundary(
	messageIDs []int64,
	rfc724MID string,
) (int, bool, error) {
	rfc724MID = strings.TrimSpace(rfc724MID)
	if rfc724MID == "" {
		return 0, false, nil
	}

	for index := len(messageIDs) - 1; index >= 0; index-- {
		candidateMID, err := c.messageRFC724MID(messageIDs[index], "")
		if err != nil {
			return 0, false, err
		}
		if candidateMID == rfc724MID {
			return index, true, nil
		}
	}
	return 0, false, nil
}

func (c *DeltaChatChannel) processMessageWithRetry(
	messageID int64,
	listenerState *deltaChatListenerState,
) deltaChatProcessState {
	backoff := deltaChatRetryInitial
	for c.IsRunning() && c.ctx.Err() == nil {
		pending := listenerState.pendingDownloads[messageID]
		requestDownload := pending == nil || !pending.downloadRequested
		knownRFC724MID := ""
		if pending != nil {
			knownRFC724MID = pending.rfc724MID
		}
		err := c.handleMessageWithDownload(messageID, requestDownload, knownRFC724MID)
		if err == nil {
			delete(listenerState.pendingDownloads, messageID)
			return deltaChatProcessComplete
		}
		var pendingErr *deltaChatDownloadPendingError
		if errors.As(err, &pendingErr) {
			pending = listenerState.recordPending(pendingErr)
			if pending != nil &&
				!requestDownload &&
				(pendingErr.downloadState == "Available" ||
					pendingErr.downloadState == "Failure") {
				// A provider change exposed a retryable terminal download
				// state. Permit exactly one fresh request in this drain.
				pending.downloadRequested = false
				continue
			}
			return deltaChatProcessDownloadPending
		}
		logger.WarnCF("deltachat", "Incoming message remains retryable", map[string]any{
			"message_id": messageID,
			"error":      err.Error(),
		})
		if !c.waitForRetry(backoff) {
			return deltaChatProcessStopped
		}
		backoff = nextDeltaChatBackoff(backoff)
	}
	return deltaChatProcessStopped
}

func (c *DeltaChatChannel) waitForRetry(delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-c.ctx.Done():
		return false
	case <-timer.C:
		return c.IsRunning()
	}
}

func nextDeltaChatBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > deltaChatRetryMaximum {
		return deltaChatRetryMaximum
	}
	return next
}

// handleMessage fetches one message, applies inbound filtering, and publishes it.
func (c *DeltaChatChannel) handleMessage(messageID int64) error {
	return c.handleMessageWithDownload(messageID, true, "")
}

func (c *DeltaChatChannel) handleMessageWithDownload(
	messageID int64,
	requestDownload bool,
	knownRFC724MID string,
) error {
	msg, err := c.getMessage(messageID)
	if err != nil {
		logger.DebugCF("deltachat", "get_message failed", map[string]any{
			"message_id": messageID,
			"error":      err.Error(),
		})
		return err
	}

	switch msg.DownloadState {
	case "", deltaChatDownloadDone:
		// Empty is accepted for compatibility with older RPC servers that did
		// not serialize downloadState.
	case "Undecipherable":
		logger.WarnCF("deltachat", "Dropping undecipherable message", map[string]any{
			"message_id": messageID,
		})
		return c.markSeenWithRetry(messageID, msg.ChatID)
	case "Available", "Failure":
		rfc724MID, infoErr := c.messageRFC724MID(messageID, knownRFC724MID)
		if infoErr != nil {
			return infoErr
		}
		requestIssued := false
		if requestDownload {
			if _, downloadErr := c.rpc.call(
				c.ctx,
				"download_full_message",
				c.accountID,
				messageID,
			); downloadErr != nil {
				return fmt.Errorf("download incomplete message %d: %w", messageID, downloadErr)
			}
			requestIssued = true
		}
		return &deltaChatDownloadPendingError{
			messageID:     messageID,
			chatID:        msg.ChatID,
			rfc724MID:     rfc724MID,
			downloadState: msg.DownloadState,
			requestIssued: requestIssued,
		}
	case "InProgress":
		rfc724MID, infoErr := c.messageRFC724MID(messageID, knownRFC724MID)
		if infoErr != nil {
			return infoErr
		}
		return &deltaChatDownloadPendingError{
			messageID:     messageID,
			chatID:        msg.ChatID,
			rfc724MID:     rfc724MID,
			downloadState: msg.DownloadState,
		}
	default:
		return fmt.Errorf(
			"message %d has unsupported download state %q",
			messageID,
			msg.DownloadState,
		)
	}

	if msg.IsInfo || (strings.TrimSpace(msg.Text) == "" && msg.File == "") {
		return c.markSeenWithRetry(messageID, msg.ChatID)
	}

	senderAddr := ""
	if msg.Sender != nil {
		senderAddr = msg.Sender.Address
	}
	if senderAddr != "" && strings.EqualFold(senderAddr, c.selfAddr) {
		logger.DebugCF("deltachat", "Drop: own message", map[string]any{"message_id": messageID})
		return c.markSeenWithRetry(messageID, msg.ChatID)
	}

	chat, err := c.getFullChat(msg.ChatID)
	if err != nil {
		logger.DebugCF("deltachat", "get_full_chat_by_id failed", map[string]any{
			"chat_id": msg.ChatID,
			"error":   err.Error(),
		})
		return err
	}
	// Device messages are core-generated notices, not real conversations.
	if chat.IsDeviceChat {
		logger.DebugCF("deltachat", "Drop: device message", map[string]any{"chat_id": msg.ChatID})
		return c.markSeenWithRetry(messageID, msg.ChatID)
	}
	isGroup := chat.ChatType != chatTypeSingle

	logger.DebugCF("deltachat", "Inbound message", map[string]any{
		"message_id": messageID,
		"chat_id":    msg.ChatID,
		"from":       senderAddr,
		"is_group":   isGroup,
		"has_file":   msg.File != "",
		"text_len":   len(strings.TrimSpace(msg.Text)),
	})

	senderName := senderAddr
	if msg.Sender != nil {
		if msg.Sender.DisplayName != "" {
			senderName = msg.Sender.DisplayName
		} else if msg.Sender.Name != "" {
			senderName = msg.Sender.Name
		}
	}
	if senderName == "" {
		senderName = "unknown"
	}

	chatID := strconv.FormatInt(msg.ChatID, 10)
	messageIDStr := strconv.FormatInt(msg.ID, 10)

	content := strings.TrimSpace(msg.Text)

	sender := bus.SenderInfo{
		Platform:    config.ChannelDeltaChat,
		PlatformID:  senderAddr,
		CanonicalID: identity.BuildCanonicalID(config.ChannelDeltaChat, senderAddr),
		Username:    senderAddr,
		DisplayName: senderName,
	}

	if !c.IsAllowedSender(sender) {
		logger.DebugCF("deltachat", "Drop: sender not in allow_from", map[string]any{
			"from": senderAddr,
		})
		return c.markSeenWithRetry(messageID, msg.ChatID)
	}

	isMentioned := false
	if isGroup {
		botName := c.config.DisplayName
		if botName == "" {
			botName = c.selfAddr
		}
		isMentioned = mentionsBot(content, botName, c.selfAddr)
		respond, cleaned := c.ShouldRespondInGroup(isMentioned, content)
		if !respond {
			logger.DebugCF("deltachat", "Drop: group trigger not satisfied", map[string]any{
				"chat_id":   msg.ChatID,
				"mentioned": isMentioned,
			})
			return c.markSeenWithRetry(messageID, msg.ChatID)
		}
		content = cleaned
	}

	// Attachment materialization happens only after authorization and group
	// policy checks. Durable event metadata contains only the safe provider
	// declaration below; it never contains the private Delta Chat blob path or
	// the ephemeral media:// reference.
	var mediaRefs []string
	var eventAttachments []bus.InboundAttachment
	if msg.File != "" {
		filename := safeDeltaChatFilename(msg.FileName, msg.File)
		eventAttachments = append(eventAttachments, bus.InboundAttachment{
			Filename:    filename,
			ContentType: strings.TrimSpace(msg.FileMime),
			Kind:        strings.TrimSpace(msg.ViewType),
			SizeBytes:   msg.FileBytes,
		})
		scope := channels.BuildMediaScope(c.Name(), chatID, messageIDStr)
		if ref := c.registerInboundFile(scope, msg); ref != "" {
			mediaRefs = append(mediaRefs, ref)
		} else {
			annotation := fmt.Sprintf("[attachment: %s]", filename)
			if content == "" {
				content = annotation
			} else {
				content += "\n" + annotation
			}
		}
	}

	// A file with no caption still warrants a turn; give the agent a minimal
	// placeholder so the message survives the empty-content guard below. Audio
	// gets a "[voice]" tag specifically, so the agent's transcription step can
	// substitute the transcript in place rather than appending it.
	if content == "" && len(mediaRefs) > 0 {
		if utils.IsAudioFile(msg.FileName, msg.FileMime) {
			content = "[voice]"
		} else {
			content = "[media]"
		}
	}
	if strings.TrimSpace(content) == "" {
		return c.markSeenWithRetry(messageID, msg.ChatID)
	}

	metadata := map[string]string{
		"platform":  config.ChannelDeltaChat,
		"chat_name": chat.Name,
	}
	if msg.File != "" {
		metadata["file_name"] = safeDeltaChatFilename(msg.FileName, msg.File)
		metadata["file_mime"] = msg.FileMime
	}

	inboundCtx := bus.InboundContext{
		Channel:                     config.ChannelDeltaChat,
		Account:                     strconv.FormatInt(c.accountID, 10),
		ChatID:                      chatID,
		SenderID:                    senderAddr,
		MessageID:                   messageIDStr,
		Mentioned:                   isMentioned,
		Raw:                         metadata,
		EventDedupeID:               "local:" + messageIDStr,
		OccurredAt:                  durableDeltaChatTimestamp(msg),
		EventSubject:                strings.TrimSpace(msg.Subject),
		ConversationName:            strings.TrimSpace(chat.Name),
		Attachments:                 eventAttachments,
		EventSenderVerified:         msg.Sender != nil && msg.Sender.IsVerified,
		EventTransportAuthenticated: msg.ShowPadlock,
	}
	if isGroup {
		inboundCtx.ChatType = "group"
	} else {
		inboundCtx.ChatType = "direct"
	}

	logger.DebugCF("deltachat", "Dispatching to agent", map[string]any{
		"chat_id":   chatID,
		"chat_type": inboundCtx.ChatType,
		"from":      senderAddr,
	})
	if err := c.HandleInboundContext(c.ctx, chatID, content, mediaRefs, inboundCtx, sender); err != nil {
		logger.ErrorCF("deltachat", "Dispatch failed; leaving message unseen", map[string]any{
			"message_id": messageID,
			"chat_id":    chatID,
			"error":      err.Error(),
		})
		return err
	}
	return c.markSeenWithRetry(messageID, msg.ChatID)
}

func (c *DeltaChatChannel) messageRFC724MID(messageID int64, known string) (string, error) {
	if known = strings.TrimSpace(known); known != "" {
		return known, nil
	}

	raw, err := c.rpc.call(
		c.ctx,
		"get_message_info_object",
		c.accountID,
		messageID,
	)
	if err != nil {
		return "", fmt.Errorf("get RFC 724 identity for Delta Chat message %d: %w", messageID, err)
	}
	var info dcMessageInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return "", fmt.Errorf("decode RFC 724 identity for Delta Chat message %d: %w", messageID, err)
	}
	info.RFC724MID = strings.TrimSpace(info.RFC724MID)
	if info.RFC724MID == "" {
		return "", fmt.Errorf("Delta Chat message %d has no RFC 724 Message-ID", messageID)
	}
	return info.RFC724MID, nil
}

func (c *DeltaChatChannel) markSeenWithRetry(messageID, chatID int64) error {
	backoff := deltaChatRetryInitial
	for c.ctx.Err() == nil {
		if _, err := c.rpc.call(
			c.ctx,
			"markseen_msgs",
			c.accountID,
			[]int64{messageID},
		); err == nil {
			return nil
		} else {
			logger.WarnCF("deltachat", "Failed to mark message seen; retrying acknowledgement", map[string]any{
				"message_id": messageID,
				"chat_id":    chatID,
				"error":      err.Error(),
			})
		}
		if !c.waitForRetry(backoff) {
			if err := c.ctx.Err(); err != nil {
				return err
			}
			return channels.ErrNotRunning
		}
		backoff = nextDeltaChatBackoff(backoff)
	}
	if err := c.ctx.Err(); err != nil {
		return err
	}
	return channels.ErrNotRunning
}

func durableDeltaChatTimestamp(message *dcMessage) *time.Time {
	if message == nil {
		return nil
	}
	// Prefer the locally observed receipt time. RFC 5322 Date is controlled by
	// the sender and must not be allowed to steer time-based automation.
	seconds := message.ReceivedTimestamp
	if seconds <= 0 {
		// Older RPC servers may omit receivedTimestamp. Keep a compatibility
		// fallback; trust policy is carried separately and never inferred from
		// either timestamp.
		seconds = message.Timestamp
	}
	if seconds <= 0 {
		return nil
	}
	timestamp := time.Unix(seconds, 0).UTC()
	if !timestamp.Equal(time.Unix(0, timestamp.UnixNano()).UTC()) {
		return nil
	}
	return &timestamp
}

func safeDeltaChatFilename(configured, localPath string) string {
	filename := strings.TrimSpace(configured)
	if filename == "" {
		filename = localPath
	}
	filename = strings.ReplaceAll(filename, `\`, "/")
	filename = filepath.Base(filename)
	if filename == "." || filename == "/" || filename == "" {
		return "attachment"
	}
	return filename
}

// registerInboundFile records an inbound attachment with the media store under
// the given scope and returns its media:// ref. Returns "" when there is no
// media store or registration fails, letting the caller fall back to an inline
// path annotation.
//
// Delta Chat stores attachments inside the account directory, next to the
// credential database — a location tools are intentionally NOT allowed to read.
// We therefore copy the single attachment out into the shared media temp dir
// (which read_file/load_image are permitted to access) and register that copy,
// so the agent can actually open the file. The copy is store-managed and deleted
// when the turn's scope is released.
func (c *DeltaChatChannel) registerInboundFile(scope string, msg *dcMessage) string {
	store := c.GetMediaStore()
	if store == nil {
		return ""
	}
	if msg.FileBytes > int64(config.DefaultMaxMediaSize) {
		logger.WarnCF("deltachat", "Inbound attachment was not materialized", map[string]any{
			"message_id":  msg.ID,
			"reason":      "size_limit",
			"size_bytes":  msg.FileBytes,
			"limit_bytes": config.DefaultMaxMediaSize,
		})
		return ""
	}
	filename := msg.FileName
	if filename == "" {
		filename = filepath.Base(msg.File)
	}

	localPath, err := copyToMediaTemp(msg.File, filename)
	if err != nil {
		logger.WarnCF("deltachat", "Inbound attachment was not materialized", map[string]any{
			"message_id": msg.ID,
			"reason":     deltaChatAttachmentFailureReason(err),
		})
		return ""
	}

	ref, err := store.Store(localPath, media.MediaMeta{
		Filename:      filename,
		ContentType:   msg.FileMime,
		Source:        config.ChannelDeltaChat,
		CleanupPolicy: media.CleanupPolicyDeleteOnCleanup,
	}, scope)
	if err != nil {
		logger.WarnCF("deltachat", "Inbound attachment was not materialized", map[string]any{
			"message_id": msg.ID,
			"reason":     "media_store_failed",
		})
		_ = os.Remove(localPath)
		return ""
	}
	return ref
}

// copyToMediaTemp copies srcPath into the shared media temp directory under a
// unique name and returns the destination path. The media temp dir is the
// location the read_file/load_image tools are permitted to read, so copying here
// makes the attachment readable without exposing Delta Chat's account directory.
func copyToMediaTemp(srcPath, filename string) (string, error) {
	return copyToMediaTempWithLimit(
		srcPath,
		filename,
		int64(config.DefaultMaxMediaSize),
	)
}

func copyToMediaTempWithLimit(
	srcPath,
	filename string,
	maxBytes int64,
) (string, error) {
	if maxBytes <= 0 {
		return "", errDeltaChatAttachmentCopy
	}
	if err := os.MkdirAll(media.TempDir(), 0o700); err != nil {
		return "", errDeltaChatAttachmentCopy
	}
	safe := utils.SanitizeFilename(filename)
	if safe == "" {
		safe = utils.SanitizeFilename(filepath.Base(srcPath))
	}
	if safe == "" {
		safe = "attachment"
	}
	dstPath := filepath.Join(media.TempDir(), uuid.NewString()[:8]+"_"+safe)

	src, err := os.Open(srcPath)
	if err != nil {
		return "", errDeltaChatAttachmentCopy
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return "", errDeltaChatAttachmentCopy
	}
	written, copyErr := io.Copy(dst, io.LimitReader(src, maxBytes+1))
	if copyErr != nil {
		dst.Close()
		_ = os.Remove(dstPath)
		return "", errDeltaChatAttachmentCopy
	}
	if written > maxBytes {
		dst.Close()
		_ = os.Remove(dstPath)
		return "", fmt.Errorf(
			"%w: maximum %d bytes",
			errDeltaChatAttachmentTooLarge,
			maxBytes,
		)
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(dstPath)
		return "", errDeltaChatAttachmentCopy
	}
	return dstPath, nil
}

func deltaChatAttachmentFailureReason(err error) string {
	if errors.Is(err, errDeltaChatAttachmentTooLarge) {
		return "size_limit"
	}
	return "copy_failed"
}

func (c *DeltaChatChannel) getMessage(messageID int64) (*dcMessage, error) {
	raw, err := c.rpc.call(c.ctx, "get_message", c.accountID, messageID)
	if err != nil {
		return nil, err
	}
	var msg dcMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func (c *DeltaChatChannel) getFullChat(chatID int64) (*dcChat, error) {
	raw, err := c.rpc.call(c.ctx, "get_full_chat_by_id", c.accountID, chatID)
	if err != nil {
		return nil, err
	}
	var chat dcChat
	if err := json.Unmarshal(raw, &chat); err != nil {
		return nil, err
	}
	return &chat, nil
}

// mentionsBot reports whether the message references the bot by display name or
// the local-part of its email address (a common addressing convention).
func mentionsBot(content, displayName, email string) bool {
	if containsMentionToken(content, displayName) {
		return true
	}
	if local, _, ok := strings.Cut(email, "@"); ok && local != "" {
		if containsMentionToken(content, "@"+local) {
			return true
		}
	}
	return false
}

func containsMentionToken(content, token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	contentRunes := []rune(strings.ToLower(content))
	tokenRunes := []rune(strings.ToLower(token))
	if len(tokenRunes) == 0 || len(tokenRunes) > len(contentRunes) {
		return false
	}
	for i := 0; i <= len(contentRunes)-len(tokenRunes); i++ {
		if !sameRunes(contentRunes[i:i+len(tokenRunes)], tokenRunes) {
			continue
		}
		before := i == 0 || !isMentionWordRune(contentRunes[i-1])
		afterIdx := i + len(tokenRunes)
		after := afterIdx >= len(contentRunes) || !isMentionWordRune(contentRunes[afterIdx])
		if before && after {
			return true
		}
	}
	return false
}

func sameRunes(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func isMentionWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}
