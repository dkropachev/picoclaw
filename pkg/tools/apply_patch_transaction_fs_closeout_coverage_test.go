package tools

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionFSCloseoutValidationAndIO(t *testing.T) {
	if _, err := applyPatchTxnWorkspaceIdentityDigest(applyPatchTxnIdentity{}); err == nil {
		t.Fatal("invalid workspace identity digest succeeded")
	}
	if _, err := applyPatchTxnIdentityFromFileInfo(nil, "regular"); err == nil {
		t.Fatal("nil file identity succeeded")
	}
	for _, path := range []string{"", "relative", " bad "} {
		if anchor, err := openApplyPatchTxnAnchor(path); err == nil || anchor != nil {
			t.Fatalf("invalid anchor %q = %#v, %v", path, anchor, err)
		}
	}
	if anchor, err := openApplyPatchTxnAnchor(filepath.Join(t.TempDir(), "missing")); err == nil || anchor != nil {
		t.Fatalf("missing anchor = %#v, %v", anchor, err)
	}
	if err := (*applyPatchTxnAnchor)(nil).revalidate(); err == nil {
		t.Fatal("nil anchor revalidation succeeded")
	}

	directory := t.TempDir()
	anchor := openApplyPatchTxnCloseoutAnchor(t, directory)
	defer anchor.Close()
	if _, _, err := applyPatchTxnCreateRegular(nil, "file", 0o600); err == nil {
		t.Fatal("nil-anchor create succeeded")
	}
	if _, _, err := applyPatchTxnCreateRegular(anchor, "../file", 0o600); err == nil {
		t.Fatal("invalid-basename create succeeded")
	}
	file, identity, createErr := applyPatchTxnCreateRegular(anchor, "file", 0o600)
	if createErr != nil {
		t.Fatal(createErr)
	}
	if err := applyPatchTxnWriteRegular(file, []byte("abcdef"), 0o640, true); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if duplicate, _, err := applyPatchTxnCreateRegular(anchor, "file", 0o600); err == nil || duplicate != nil {
		t.Fatalf("duplicate create = %#v, %v", duplicate, err)
	}
	if _, _, _, err := applyPatchTxnReadRegular(nil, "file", 10); err == nil {
		t.Fatal("nil-anchor read succeeded")
	}
	if _, _, _, err := applyPatchTxnReadRegular(anchor, "../file", 10); err == nil {
		t.Fatal("invalid-basename read succeeded")
	}
	if _, _, _, err := applyPatchTxnReadRegular(anchor, "file", -1); err == nil {
		t.Fatal("negative-limit read succeeded")
	}
	if _, _, _, err := applyPatchTxnReadRegular(anchor, "file", 3); err == nil {
		t.Fatal("oversize read succeeded")
	}
	if err := os.Mkdir(filepath.Join(directory, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := applyPatchTxnReadRegular(anchor, "child", 10); err == nil {
		t.Fatal("directory regular-read succeeded")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	open, openErr := os.OpenFile(filepath.Join(directory, "write-canceled"), os.O_CREATE|os.O_RDWR, 0o600)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if err := applyPatchTxnWriteRegularContext(
		canceled, open, []byte("x"), 0o600, true,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write = %v", err)
	}
	_ = open.Close()
	if err := applyPatchTxnWriteRegularContext(nil, nil, nil, 0o600, false); err == nil {
		t.Fatal("nil stage write succeeded")
	}
	closed, tempErr := os.CreateTemp(directory, "closed-")
	if tempErr != nil {
		t.Fatal(tempErr)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := applyPatchTxnWriteRegularContext(context.Background(), closed, []byte("x"), 0o600, false); err == nil {
		t.Fatal("closed stage write succeeded")
	}
	if err := applyPatchTxnWriteRegularContext(context.Background(), closed, nil, 0o600, true); err == nil {
		t.Fatal("closed stage chmod succeeded")
	}
	if err := applyPatchTxnWriteRegularContext(context.Background(), closed, nil, 0o600, false); err == nil {
		t.Fatal("closed stage sync succeeded")
	}

	if err := applyPatchTxnResumeRegularContext(
		context.Background(), anchor, "file", identity, []byte("abcXef"), 0o640,
	); err == nil {
		t.Fatal("conflicting resume content succeeded")
	}
	wrongIdentity := identity
	wrongIdentity.File++
	if err := applyPatchTxnResumeRegularContext(
		context.Background(), anchor, "file", wrongIdentity, []byte("abcdef"), 0o640,
	); err == nil {
		t.Fatal("wrong-identity resume succeeded")
	}
	partial, partialIdentity, partialErr := applyPatchTxnCreateRegular(anchor, "partial", 0o600)
	if partialErr != nil {
		t.Fatal(partialErr)
	}
	if _, err := partial.WriteString("abc"); err != nil {
		t.Fatal(err)
	}
	if err := partial.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(directory, "partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyPatchTxnResumeRegularContext(
		context.Background(), anchor, "partial", partialIdentity, []byte("abcdef"), 0o600,
	); err == nil {
		t.Fatal("conflicting resume mode succeeded")
	}
	if err := os.Chmod(filepath.Join(directory, "partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyPatchTxnResumeRegularContext(
		context.Background(), anchor, "partial", partialIdentity, []byte("abcdef"), 0o640,
	); err != nil {
		t.Fatalf("valid resume = %v", err)
	}
}

func TestApplyPatchTransactionFSCloseoutNamespacePrimitives(t *testing.T) {
	directory := t.TempDir()
	anchor := openApplyPatchTxnCloseoutAnchor(t, directory)
	defer anchor.Close()
	file, identity, err := applyPatchTxnCreateRegular(anchor, "source", 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if err := applyPatchTxnLinkNoReplace(nil, "source", anchor, "linked"); err == nil {
		t.Fatal("nil-anchor link succeeded")
	}
	if err := applyPatchTxnLinkNoReplace(anchor, "../source", anchor, "linked"); err == nil {
		t.Fatal("invalid source-name link succeeded")
	}
	if err := applyPatchTxnLinkNoReplace(anchor, "source", anchor, "../linked"); err == nil {
		t.Fatal("invalid target-name link succeeded")
	}
	if err := applyPatchTxnLinkNoReplace(anchor, "source", anchor, "linked"); err != nil {
		t.Fatal(err)
	}
	if err := applyPatchTxnLinkNoReplace(anchor, "source", anchor, "linked"); err == nil {
		t.Fatal("replacing link succeeded")
	}
	if err := applyPatchTxnLinkWitness(
		anchor, "source", applyPatchTxnIdentity{}, 3,
		anchor, "witness", applyPatchTxnCloseoutPrivateName(t, "remove"),
	); err == nil {
		t.Fatal("invalid witness identity succeeded")
	}
	wrong := identity
	wrong.File++
	cleanupName := applyPatchTxnCloseoutPrivateName(t, "remove")
	if err := applyPatchTxnLinkWitness(
		anchor, "source", wrong, 3, anchor, "wrong-witness", cleanupName,
	); err == nil {
		t.Fatal("wrong witness binding succeeded")
	}
	if _, err := os.Lstat(filepath.Join(directory, "wrong-witness")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrong witness cleanup = %v", err)
	}

	if err := applyPatchTxnRenameNoReplace(nil, "source", anchor, "renamed"); err == nil {
		t.Fatal("nil-anchor rename succeeded")
	}
	if err := applyPatchTxnRenameNoReplace(anchor, "../source", anchor, "renamed"); err == nil {
		t.Fatal("invalid source-name rename succeeded")
	}
	if err := applyPatchTxnRenameNoReplace(anchor, "source", anchor, "../renamed"); err == nil {
		t.Fatal("invalid target-name rename succeeded")
	}
	if err := applyPatchTxnRenameNoReplace(anchor, "source", anchor, "linked"); err == nil {
		t.Fatal("replacing rename succeeded")
	}

	if _, err := applyPatchTxnMkdir(nil, "directory", 0o700); err == nil {
		t.Fatal("nil-anchor mkdir succeeded")
	}
	if _, err := applyPatchTxnMkdir(anchor, "../directory", 0o700); err == nil {
		t.Fatal("invalid-name mkdir succeeded")
	}
	if _, err := applyPatchTxnMkdir(anchor, "linked", 0o700); err == nil {
		t.Fatal("colliding mkdir succeeded")
	}
	if child, err := applyPatchTxnOpenChildDirectory(nil, "child"); err == nil || child != nil {
		t.Fatalf("nil child open = %#v, %v", child, err)
	}
	if child, err := applyPatchTxnOpenChildDirectory(anchor, "../child"); err == nil || child != nil {
		t.Fatalf("invalid child open = %#v, %v", child, err)
	}
	if child, err := applyPatchTxnOpenChildDirectory(anchor, "source"); err == nil || child != nil {
		t.Fatalf("file child open = %#v, %v", child, err)
	}
	if err := applyPatchTxnSyncDirectory(nil); err == nil {
		t.Fatal("nil directory sync succeeded")
	}
	if _, err := applyPatchTxnReadDirectoryNames(nil, 1); err == nil {
		t.Fatal("nil directory listing succeeded")
	}
	if _, err := applyPatchTxnReadDirectoryNames(anchor, -1); err == nil {
		t.Fatal("negative directory listing succeeded")
	}
	if _, err := applyPatchTxnReadDirectoryNames(anchor, applyPatchTransactionMaxEntries+1); err == nil {
		t.Fatal("oversize directory listing succeeded")
	}
	if _, err := applyPatchTxnReadDirectoryNames(anchor, 0); err == nil {
		t.Fatal("alien directory listing succeeded")
	}
	if err := applyPatchTxnProbeNoReplace(nil, "source", identity); err == nil {
		t.Fatal("nil probe succeeded")
	}
	if err := applyPatchTxnProbeNoReplace(anchor, "../source", identity); err == nil {
		t.Fatal("invalid-name probe succeeded")
	}
	if err := applyPatchTxnProbeNoReplace(anchor, "source", wrong); err == nil {
		t.Fatal("wrong-identity probe succeeded")
	}
	if err := requireApplyPatchTxnAnchors(anchor, nil); err == nil {
		t.Fatal("nil anchor set succeeded")
	}
}

func TestApplyPatchTransactionFSCloseoutRemovalAndQuarantine(t *testing.T) {
	t.Run("guard failures", func(t *testing.T) {
		if err := applyPatchTxnRemoveExact(nil, "source", "remove", applyPatchTxnIdentity{}, false); err == nil {
			t.Fatal("nil-anchor removal succeeded")
		}
		directory := t.TempDir()
		anchor := openApplyPatchTxnCloseoutAnchor(t, directory)
		defer anchor.Close()
		if err := applyPatchTxnRemoveExact(anchor, "../source", "remove", applyPatchTxnIdentity{}, false); err == nil {
			t.Fatal("invalid-name removal succeeded")
		}
		if err := applyPatchTxnRemoveExact(anchor, "source", "remove", applyPatchTxnIdentity{}, false); err == nil {
			t.Fatal("invalid removal-private-name succeeded")
		}
		if err := applyPatchTxnQuarantineExact(nil, "source", "quarantine", applyPatchTxnIdentity{}); err == nil {
			t.Fatal("nil-anchor quarantine succeeded")
		}
		if err := applyPatchTxnQuarantineExact(anchor, "../source", "quarantine", applyPatchTxnIdentity{}); err == nil {
			t.Fatal("invalid source quarantine succeeded")
		}
		if err := applyPatchTxnQuarantineExact(anchor, "source", "../quarantine", applyPatchTxnIdentity{}); err == nil {
			t.Fatal("invalid target quarantine succeeded")
		}
	})

	t.Run("wrong kind restores source", func(t *testing.T) {
		directory := t.TempDir()
		anchor := openApplyPatchTxnCloseoutAnchor(t, directory)
		defer anchor.Close()
		identity, err := applyPatchTxnMkdir(anchor, "source", 0o700)
		if err != nil {
			t.Fatal(err)
		}
		if err := applyPatchTxnRemoveExact(
			anchor, "source", applyPatchTxnCloseoutPrivateName(t, "remove"), identity, false,
		); err == nil {
			t.Fatal("directory removed as regular file")
		}
		if info, err := os.Lstat(filepath.Join(directory, "source")); err != nil || !info.IsDir() {
			t.Fatalf("wrong-kind source restore = %#v, %v", info, err)
		}
	})

	t.Run("source reappears beside removal", func(t *testing.T) {
		directory := t.TempDir()
		anchor := openApplyPatchTxnCloseoutAnchor(t, directory)
		defer anchor.Close()
		file, identity, err := applyPatchTxnCreateRegular(anchor, "source", 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
		removalName := applyPatchTxnCloseoutPrivateName(t, "remove")
		if err := applyPatchTxnLinkNoReplace(anchor, "source", anchor, removalName); err != nil {
			t.Fatal(err)
		}
		if err := applyPatchTxnRemoveExact(anchor, "source", removalName, identity, false); err == nil {
			t.Fatal("reappeared removal source succeeded")
		}
	})

	t.Run("post-quarantine interruption and mutation", func(t *testing.T) {
		for _, mutate := range []bool{false, true} {
			directory := t.TempDir()
			anchor := openApplyPatchTxnCloseoutAnchor(t, directory)
			file, identity, createErr := applyPatchTxnCreateRegular(anchor, "source", 0o600)
			if createErr != nil {
				t.Fatal(createErr)
			}
			_ = file.Close()
			removalName := applyPatchTxnCloseoutPrivateName(t, "remove")
			injected := errors.New("post-quarantine interruption")
			removeErr := applyPatchTxnRemoveExact(
				anchor,
				"source",
				removalName,
				identity,
				false,
				func() error {
					if !mutate {
						return injected
					}
					if unlinkErr := os.Remove(filepath.Join(directory, removalName)); unlinkErr != nil {
						return unlinkErr
					}
					return os.WriteFile(filepath.Join(directory, removalName), []byte("alien"), 0o600)
				},
			)
			if !mutate && !errors.Is(removeErr, injected) {
				t.Fatalf("interrupted removal = %v", removeErr)
			}
			if mutate && removeErr == nil {
				t.Fatal("mutated removal quarantine succeeded")
			}
			_ = anchor.Close()
		}
	})

	t.Run("wrong quarantine identity restores", func(t *testing.T) {
		directory := t.TempDir()
		anchor := openApplyPatchTxnCloseoutAnchor(t, directory)
		defer anchor.Close()
		file, identity, err := applyPatchTxnCreateRegular(anchor, "source", 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
		wrong := identity
		wrong.File++
		if err := applyPatchTxnQuarantineExact(anchor, "source", "quarantine", wrong); err == nil {
			t.Fatal("wrong quarantine identity succeeded")
		}
		if _, err := os.Lstat(filepath.Join(directory, "source")); err != nil {
			t.Fatalf("wrong-identity quarantine did not restore source: %v", err)
		}
	})

	if got := wrapApplyPatchTxnRestoreError(nil); got != nil {
		t.Fatalf("nil restore wrapper = %v", got)
	}
	injected := errors.New("restore failed")
	if got := wrapApplyPatchTxnRestoreError(injected); !errors.Is(got, injected) {
		t.Fatalf("restore wrapper = %v", got)
	}
}

func openApplyPatchTxnCloseoutAnchor(t *testing.T, directory string) *applyPatchTxnAnchor {
	t.Helper()
	anchor, err := openApplyPatchTxnAnchor(directory)
	if err != nil {
		t.Fatal(err)
	}
	return anchor
}

func applyPatchTxnCloseoutPrivateName(t *testing.T, role string) string {
	t.Helper()
	name, err := newApplyPatchTxnPrivateName(role)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func TestApplyPatchTransactionFSCloseoutStaleNamedAnchor(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "anchor")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	anchor := openApplyPatchTxnCloseoutAnchor(t, path)
	defer anchor.Close()
	if err := os.Rename(path, path+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := anchor.revalidate(); err == nil {
		t.Fatal("stale named anchor revalidated")
	}
}

func TestApplyPatchTransactionFSCloseoutSpecialReadError(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "pipe")
	if err := createApplyPatchTxnCloseoutFIFO(path); err != nil {
		if errors.Is(err, errors.ErrUnsupported) {
			t.Skip(err.Error())
		}
		t.Fatal(err)
	}
	anchor := openApplyPatchTxnCloseoutAnchor(t, directory)
	defer anchor.Close()
	if _, _, _, err := applyPatchTxnReadRegular(anchor, "pipe", 1); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("FIFO regular-read error = %v", err)
	}
}
