package storecatalog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestRevalidateRejectsUnavailableHomeAndCollidingProjection(t *testing.T) {
	if catalog, err := Revalidate(nil); catalog != nil || err == nil {
		t.Fatalf("Revalidate(nil) = %#v, %v", catalog, err)
	}
	missingHome := filepath.Join(t.TempDir(), "missing")
	if catalog, err := Revalidate(&Catalog{
		home:  missingHome,
		specs: []Spec{{ID: "global/auth", Domain: "auth", Path: filepath.Join(missingHome, "auth.db")}},
	}); catalog != nil || err == nil {
		t.Fatalf("Revalidate(missing home) = %#v, %v", catalog, err)
	}
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(home, "shared.db")
	if catalog, err := Revalidate(&Catalog{
		home: home,
		specs: []Spec{
			{ID: "global/auth", Domain: "auth", Path: shared},
			{ID: "launcher/auth", Domain: "launcher-auth", Path: shared},
		},
	}); catalog != nil || err == nil {
		t.Fatalf("Revalidate(colliding projection) = %#v, %v", catalog, err)
	}
	projected, err := Project(Options{Home: home, Config: &config.Config{}})
	if err != nil {
		t.Fatal(err)
	}
	projected.specs[0].ID = "GLOBAL/auth"
	if catalog, err := Revalidate(projected); catalog != nil || err == nil {
		t.Fatalf("Revalidate(invalid spec) = %#v, %v", catalog, err)
	}
}
