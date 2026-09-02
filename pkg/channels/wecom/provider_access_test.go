package wecom

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
)

func init() {
	allowUnfencedWeComProviderForTests.Store(true)
}

func TestWeComProviderRejectsMissingBrokerAuthority(t *testing.T) {
	restoreAuthority := database.SuspendProviderTestAuthority()
	t.Cleanup(restoreAuthority)
	allowUnfencedWeComProviderForTests.Store(false)
	t.Cleanup(func() { allowUnfencedWeComProviderForTests.Store(true) })
	path := filepath.Join(t.TempDir(), "reqid-store.db")
	handler := NewBrokerHandler(filepath.Dir(path))
	if handler.home != "" || database.CodeOf(handler.err) != database.CodeUnauthorized {
		t.Fatalf("unfenced WeCom handler = %#v", handler)
	}
	store := newReqIDStore(path)
	if database.CodeOf(store.initializationError()) != database.CodeUnavailable {
		t.Fatalf("unfenced WeCom store error = %v", store.initializationError())
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("unfenced WeCom store created database: %v", err)
	}
}
