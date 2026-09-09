package databasereadiness

import (
	"errors"
	"os"
	"testing"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func TestPinnedLegacyRevalidationClosesEveryReopenedHandleOnFailure(t *testing.T) {
	path := t.TempDir() + string(os.PathSeparator) + "legacy.json"
	writeReadinessFile(t, path, []byte("legacy"))
	identity, objectType, exists, err := fileidentity.ExistingWithType(path)
	if err != nil || !exists {
		t.Fatalf("legacy identity = %#v, %v, %t, %v", identity, objectType, exists, err)
	}
	canary := errors.New("retained identity canary")

	for _, test := range []struct {
		name       string
		openedFail bool
		reopenErr  error
		exists     bool
		want       error
	}{
		{name: "retained identity failure", openedFail: true, exists: true, want: canary},
		{name: "reopen failure with handle", reopenErr: canary, exists: true, want: canary},
		{name: "missing result with handle", exists: false, want: errLegacyIntegrity},
	} {
		t.Run(test.name, func(t *testing.T) {
			retained, openErr := os.Open(path)
			if openErr != nil {
				t.Fatal(openErr)
			}
			pinned := &pinnedLegacyFile{
				file: retained, path: path, identity: identity, objectType: objectType,
			}
			t.Cleanup(func() { _ = pinned.Close() })
			reopened, openErr := os.Open(path)
			if openErr != nil {
				t.Fatal(openErr)
			}
			opened := func(file *os.File) (fileidentity.Identity, fileidentity.ObjectType, error) {
				if test.openedFail && file == retained {
					return fileidentity.Identity{}, fileidentity.ObjectType(0), canary
				}
				return fileidentity.Opened(file)
			}
			reopen := func(string, fileidentity.ObjectType) (*os.File, bool, error) {
				return reopened, test.exists, test.reopenErr
			}
			revalidateErr := pinned.revalidateNamedWith(opened, reopen)
			if !errors.Is(revalidateErr, test.want) {
				t.Fatalf("revalidation error = %v, want %v", revalidateErr, test.want)
			}
			if _, statErr := reopened.Stat(); !errors.Is(statErr, os.ErrClosed) {
				t.Fatalf("reopened handle remained live: %v", statErr)
			}
		})
	}
}
