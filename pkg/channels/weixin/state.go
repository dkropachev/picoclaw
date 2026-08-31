package weixin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	basechannels "github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	weixinDefaultCDNBaseURL    = "https://novac2c.cdn.weixin.qq.com/c2c"
	weixinConfigCacheTTL       = 24 * time.Hour
	weixinConfigRetryInitial   = 2 * time.Second
	weixinConfigRetryMax       = time.Hour
	weixinSessionPauseDuration = time.Hour
	weixinSessionExpiredCode   = -14
)

type typingTicketCacheEntry struct {
	ticket      string
	nextFetchAt time.Time
	retryDelay  time.Duration
}

type syncCursorFile struct {
	GetUpdatesBuf string `json:"get_updates_buf"`
}

func picoclawHomeDir() string {
	return config.GetHome()
}

func genWeixinAccountKey(cfg *config.WeixinSettings) string {
	token := strings.TrimSpace(cfg.Token.String())
	if token == "" {
		return "default"
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(cfg.BaseURL) + "|" + token))
	return hex.EncodeToString(sum[:8])
}

func buildWeixinSyncBufPath(cfg *config.WeixinSettings) string {
	return filepath.Join(picoclawHomeDir(), "channels", "weixin", "sync", genWeixinAccountKey(cfg)+".json")
}

func buildWeixinContextTokensPath(cfg *config.WeixinSettings) string {
	return filepath.Join(picoclawHomeDir(), "channels", "weixin", "context-tokens", genWeixinAccountKey(cfg)+".json")
}

func loadGetUpdatesBuf(path string) (string, error) {
	store, err := newWeixinStateStore(path, weixinStateKindCursor)
	if err != nil {
		return "", err
	}
	return store.loadCursor(context.Background())
}

func saveGetUpdatesBuf(path, cursor string) error {
	store, err := newWeixinStateStore(path, weixinStateKindCursor)
	if err != nil {
		return err
	}
	return store.saveCursor(context.Background(), cursor)
}

func loadContextTokens(path string) (map[string]string, error) {
	store, err := newWeixinStateStore(path, weixinStateKindTokens)
	if err != nil {
		return nil, err
	}
	return store.loadTokens(context.Background())
}

func saveContextTokens(path string, tokens map[string]string) error {
	store, err := newWeixinStateStore(path, weixinStateKindTokens)
	if err != nil {
		return err
	}
	return store.saveTokens(context.Background(), tokens)
}

func saveContextToken(path, userID, token string) error {
	store, err := newWeixinStateStore(path, weixinStateKindTokens)
	if err != nil {
		return err
	}
	return store.saveToken(context.Background(), userID, token)
}

func (c *WeixinChannel) cdnBaseURL() string {
	if base := strings.TrimSpace(c.config.CDNBaseURL); base != "" {
		return strings.TrimRight(base, "/")
	}
	return weixinDefaultCDNBaseURL
}

func isSessionExpiredStatus(ret, errcode int) bool {
	return ret == weixinSessionExpiredCode || errcode == weixinSessionExpiredCode
}

func (c *WeixinChannel) pauseSession(operation string, ret, errcode int, errmsg string) time.Duration {
	c.pauseMu.Lock()
	defer c.pauseMu.Unlock()

	until := time.Now().Add(weixinSessionPauseDuration)
	if until.After(c.pauseUntil) {
		c.pauseUntil = until
	}

	remaining := time.Until(c.pauseUntil)
	logger.ErrorCF("weixin", "Session expired; pausing Weixin channel", map[string]any{
		"operation": operation,
		"ret":       ret,
		"errcode":   errcode,
		"errmsg":    errmsg,
		"until":     c.pauseUntil.Format(time.RFC3339),
		"minutes":   int((remaining + time.Minute - 1) / time.Minute),
	})
	return remaining
}

func (c *WeixinChannel) remainingPause() time.Duration {
	c.pauseMu.Lock()
	defer c.pauseMu.Unlock()

	if c.pauseUntil.IsZero() {
		return 0
	}
	remaining := time.Until(c.pauseUntil)
	if remaining <= 0 {
		c.pauseUntil = time.Time{}
		return 0
	}
	return remaining
}

func (c *WeixinChannel) waitWhileSessionPaused(ctx context.Context) error {
	remaining := c.remainingPause()
	if remaining <= 0 {
		return nil
	}

	timer := time.NewTimer(remaining)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *WeixinChannel) ensureSessionActive() error {
	remaining := c.remainingPause()
	if remaining <= 0 {
		return nil
	}
	return fmt.Errorf(
		"weixin session paused (%d min remaining): %w",
		int((remaining+time.Minute-1)/time.Minute),
		basechannels.ErrSendFailed,
	)
}

func (c *WeixinChannel) getTypingTicket(ctx context.Context, userID string) (string, error) {
	now := time.Now()

	c.typingMu.Lock()
	entry, ok := c.typingCache[userID]
	if ok && now.Before(entry.nextFetchAt) {
		ticket := entry.ticket
		c.typingMu.Unlock()
		return ticket, nil
	}
	cachedTicket := entry.ticket
	retryDelay := entry.retryDelay
	c.typingMu.Unlock()

	contextToken := ""
	if v, ok := c.contextTokens.Load(userID); ok {
		contextToken, _ = v.(string)
	}

	resp, err := c.api.GetConfig(ctx, GetConfigReq{
		IlinkUserID:  userID,
		ContextToken: contextToken,
	})
	if err == nil && resp != nil && resp.Ret == 0 && resp.Errcode == 0 {
		ticket := strings.TrimSpace(resp.TypingTicket)
		c.typingMu.Lock()
		c.typingCache[userID] = typingTicketCacheEntry{
			ticket:      ticket,
			nextFetchAt: now.Add(weixinConfigCacheTTL),
			retryDelay:  weixinConfigRetryInitial,
		}
		c.typingMu.Unlock()
		return ticket, nil
	}

	if resp != nil && isSessionExpiredStatus(resp.Ret, resp.Errcode) {
		c.pauseSession("getconfig", resp.Ret, resp.Errcode, resp.Errmsg)
	}

	if retryDelay <= 0 {
		retryDelay = weixinConfigRetryInitial
	} else {
		retryDelay *= 2
		if retryDelay > weixinConfigRetryMax {
			retryDelay = weixinConfigRetryMax
		}
	}

	c.typingMu.Lock()
	c.typingCache[userID] = typingTicketCacheEntry{
		ticket:      cachedTicket,
		nextFetchAt: now.Add(retryDelay),
		retryDelay:  retryDelay,
	}
	c.typingMu.Unlock()

	if err != nil {
		return cachedTicket, err
	}
	if resp == nil {
		return cachedTicket, fmt.Errorf("getconfig returned nil response")
	}
	return cachedTicket, fmt.Errorf(
		"getconfig failed: ret=%d errcode=%d errmsg=%s",
		resp.Ret,
		resp.Errcode,
		resp.Errmsg,
	)
}
