package weixin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
)

func init() {
	allowUnfencedWeixinProviderForTests.Store(true)
}

func TestWeixinProviderRejectsMissingBrokerAuthority(t *testing.T) {
	restoreAuthority := database.SuspendProviderTestAuthority()
	t.Cleanup(restoreAuthority)
	allowUnfencedWeixinProviderForTests.Store(false)
	t.Cleanup(func() { allowUnfencedWeixinProviderForTests.Store(true) })
	root := t.TempDir()
	locator := filepath.Join(root, "sync", "default.json")
	handler := NewBrokerHandler(root)
	if handler.home != "" || database.CodeOf(handler.err) != database.CodeUnauthorized {
		t.Fatalf("unfenced Weixin handler = %#v", handler)
	}
	if _, err := newWeixinStateStore(locator, weixinStateKindCursor); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("unfenced Weixin store error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, weixinStateDatabaseFilename)); !os.IsNotExist(err) {
		t.Fatalf("unfenced Weixin store created database: %v", err)
	}
}
