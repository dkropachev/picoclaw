package tools

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestApplyPatchTransactionTargetLayoutExistingAndMissingParents(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		target     string
		wantAnchor string
		wantParts  []string
	}{
		{
			name: "existing parent", target: filepath.Join(existing, "file.txt"),
			wantAnchor: existing, wantParts: []string{"file.txt"},
		},
		{
			name: "missing forest", target: filepath.Join(existing, "one", "two", "file.txt"),
			wantAnchor: existing, wantParts: []string{"one", "two", "file.txt"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			layout, err := resolveApplyPatchTxnTargetLayout(test.target)
			if err != nil {
				t.Fatalf("resolveApplyPatchTxnTargetLayout() error = %v", err)
			}
			if layout.anchorPath != test.wantAnchor || !reflect.DeepEqual(layout.components, test.wantParts) {
				t.Fatalf("layout = %#v, want anchor=%q components=%#v", layout, test.wantAnchor, test.wantParts)
			}
		})
	}
}

func TestApplyPatchTransactionTargetLayoutRejectsUnsafeAncestor(t *testing.T) {
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink("real", alias); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveApplyPatchTxnTargetLayout(filepath.Join(alias, "file")); err == nil {
		t.Fatal("symlink ancestor was accepted")
	}
	ordinary := filepath.Join(root, "ordinary")
	if err := os.WriteFile(ordinary, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveApplyPatchTxnTargetLayout(filepath.Join(ordinary, "file")); err == nil {
		t.Fatal("regular-file ancestor was accepted")
	}
}

func TestApplyPatchTransactionPrivateNamesAndGrouping(t *testing.T) {
	first, err := newApplyPatchTxnPrivateName("stage")
	if err != nil {
		t.Fatal(err)
	}
	second, err := newApplyPatchTxnPrivateName("stage")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.HasPrefix(first, ".picoclaw-apply-patch-stage-") ||
		len(first) != len(".picoclaw-apply-patch-stage-")+32 {
		t.Fatalf("private names = %q and %q", first, second)
	}
	_, invalidNameErr := newApplyPatchTxnPrivateName("bad/name")
	if invalidNameErr == nil {
		t.Fatal("unsafe private-name prefix accepted")
	}
	key, err := applyPatchTxnLayoutGroupKey(applyPatchTxnTargetLayout{
		anchorPath: "/tmp/root", components: []string{"missing", "file"},
	})
	if err != nil || key != filepath.Clean("/tmp/root")+"\x00missing" {
		t.Fatalf("group key = %q, %v", key, err)
	}
}

func TestApplyPatchTransactionExistingRegularEndpointPinsIdentity(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "source")
	if err := os.WriteFile(path, []byte("source"), 0o640); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := openApplyPatchTxnExistingRegular(path, info)
	if err != nil {
		t.Fatalf("openApplyPatchTxnExistingRegular() error = %v", err)
	}
	if endpoint.basename != "source" || endpoint.state.Mode.Perm() != 0o640 ||
		endpoint.state.Links != 1 {
		t.Fatalf("endpoint = %+v", endpoint)
	}
	closeErr := endpoint.Close()
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	held, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	removeErr := os.Remove(path)
	if removeErr != nil {
		t.Fatal(removeErr)
	}
	writeErr := os.WriteFile(path, []byte("source"), 0o640)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	replacementEndpoint, replacementErr := openApplyPatchTxnExistingRegular(path, info)
	if replacementErr == nil {
		_ = replacementEndpoint.Close()
		t.Fatal("identical-content ABA replacement was accepted")
	}
}
