//go:build unix && !aix

package databasereadiness

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func TestLegacyPinnedPathFaultBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(path, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, objectType, exists, identityErr := fileidentity.ExistingWithType(path)
	if identityErr != nil || !exists {
		t.Fatalf("fixture identity = %#v, %v, %t, %v", identity, objectType, exists, identityErr)
	}
	lookup := func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
		return identity, objectType, true, nil
	}
	canary := errors.New("legacy path fault canary")

	if resultFile, found, openErr := openPinnedLegacyPathWith(path, nil, openLegacyNoFollow); resultFile != nil ||
		found || !errors.Is(openErr, errLegacyIntegrity) {
		t.Fatalf("nil lookup = %#v, %t, %v", resultFile, found, openErr)
	}
	if resultFile, found, openErr := openPinnedLegacyPathWith(path, lookup, nil); resultFile != nil ||
		found || !errors.Is(openErr, errLegacyIntegrity) {
		t.Fatalf("nil opener = %#v, %t, %v", resultFile, found, openErr)
	}

	missingLookup := func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
		return fileidentity.Identity{}, 0, false, nil
	}
	var injected *os.File
	resultFile, found, gotErr := openPinnedLegacyPathWith(
		path,
		missingLookup,
		func(string, fileidentity.ObjectType) (*os.File, bool, error) {
			injected, identityErr = os.Open(path)
			if injected != nil {
				opened := injected
				t.Cleanup(func() { _ = opened.Close() })
			}
			return injected, false, identityErr
		},
	)
	assertLegacyHandleClosed(t, injected)
	if resultFile != nil || found || !errors.Is(gotErr, errLegacyIntegrity) {
		t.Fatalf("missing lookup with handle = %#v, %t, %v", resultFile, found, gotErr)
	}
	injected = nil
	if nextFile, nextFound, openErr := openPinnedLegacyPathWith(
		path,
		missingLookup,
		func(string, fileidentity.ObjectType) (*os.File, bool, error) { return nil, true, nil },
	); nextFile != nil || nextFound || !errors.Is(openErr, errLegacyIntegrity) {
		t.Fatalf("materialized missing lookup = %#v, %t, %v", nextFile, nextFound, openErr)
	}
	if nextFile, nextFound, openErr := openPinnedLegacyPathWith(
		path,
		missingLookup,
		func(string, fileidentity.ObjectType) (*os.File, bool, error) { return nil, false, canary },
	); nextFile != nil || nextFound || !errors.Is(openErr, canary) {
		t.Fatalf("missing lookup open error = %#v, %t, %v", nextFile, nextFound, openErr)
	}

	if nextFile, nextFound, openErr := openPinnedLegacyPathWith(
		path,
		lookup,
		func(string, fileidentity.ObjectType) (*os.File, bool, error) { return nil, false, nil },
	); nextFile != nil || nextFound || !errors.Is(openErr, errLegacyIntegrity) {
		t.Fatalf("disappeared existing path = %#v, %t, %v", nextFile, nextFound, openErr)
	}
	resultFile, found, gotErr = openPinnedLegacyPathWith(
		path,
		lookup,
		func(string, fileidentity.ObjectType) (*os.File, bool, error) {
			injected, identityErr = os.Open(path)
			if injected != nil {
				opened := injected
				t.Cleanup(func() { _ = opened.Close() })
			}
			return injected, true, errors.Join(identityErr, canary)
		},
	)
	assertLegacyHandleClosed(t, injected)
	if resultFile != nil || found || !errors.Is(gotErr, canary) {
		t.Fatalf("existing path open error = %#v, %t, %v", resultFile, found, gotErr)
	}
	other := filepath.Join(t.TempDir(), "other.json")
	if writeErr := os.WriteFile(other, []byte("other"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	injected = nil
	resultFile, found, gotErr = openPinnedLegacyPathWith(
		path,
		lookup,
		func(string, fileidentity.ObjectType) (*os.File, bool, error) {
			injected, identityErr = os.Open(other)
			if injected != nil {
				opened := injected
				t.Cleanup(func() { _ = opened.Close() })
			}
			return injected, true, identityErr
		},
	)
	assertLegacyHandleClosed(t, injected)
	if resultFile != nil || found || !errors.Is(gotErr, errLegacyIntegrity) {
		t.Fatalf("mismatched opened path = %#v, %t, %v", resultFile, found, gotErr)
	}
}

func TestLegacyPinnedChildFaultBoundaries(t *testing.T) {
	newParent := func(t *testing.T) (*pinnedLegacyFile, string) {
		t.Helper()
		root := t.TempDir()
		child := filepath.Join(root, "child.json")
		if err := os.WriteFile(child, []byte("child"), 0o600); err != nil {
			t.Fatal(err)
		}
		parent, exists, err := openPinnedLegacyPath(root)
		if err != nil || !exists {
			t.Fatalf("pin parent = %#v, %t, %v", parent, exists, err)
		}
		t.Cleanup(func() { _ = parent.Close() })
		return parent, child
	}
	canary := errors.New("legacy child fault canary")
	if child, err := openPinnedLegacyChildWith(nil, "child", nil, nil); child != nil ||
		!errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("invalid child boundary = %#v, %v", child, err)
	}

	t.Run("parent changed", func(t *testing.T) {
		parent, _ := newParent(t)
		if err := os.Rename(parent.path, parent.path+".moved"); err != nil {
			t.Fatal(err)
		}
		if child, err := openPinnedLegacyChild(parent, "child.json"); child != nil ||
			!errors.Is(err, errLegacyIntegrity) {
			t.Fatalf("changed parent = %#v, %v", child, err)
		}
	})

	t.Run("lookup", func(t *testing.T) {
		parent, _ := newParent(t)
		lookup := func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
			return fileidentity.Identity{}, 0, false, canary
		}
		if child, err := openPinnedLegacyChildWith(
			parent, "child.json", lookup, openLegacyChildNoFollow,
		); child != nil || !errors.Is(err, canary) {
			t.Fatalf("child lookup error = %#v, %v", child, err)
		}
	})

	t.Run("missing", func(t *testing.T) {
		parent, _ := newParent(t)
		lookup := func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
			return fileidentity.Identity{}, 0, false, nil
		}
		if child, err := openPinnedLegacyChildWith(
			parent, "child.json", lookup, openLegacyChildNoFollow,
		); child != nil || !errors.Is(err, errLegacyIntegrity) {
			t.Fatalf("missing child = %#v, %v", child, err)
		}
	})

	for _, test := range []struct {
		name   string
		opener legacyChildOpen
		want   error
	}{
		{
			name: "open error with handle",
			opener: func(_ *os.File, path, _ string, _ fileidentity.ObjectType) (*os.File, bool, error) {
				opened, openErr := os.Open(path)
				return opened, true, errors.Join(openErr, canary)
			},
			want: canary,
		},
		{
			name: "disappeared with handle",
			opener: func(_ *os.File, path, _ string, _ fileidentity.ObjectType) (*os.File, bool, error) {
				opened, openErr := os.Open(path)
				return opened, false, openErr
			},
			want: errLegacyIntegrity,
		},
		{
			name: "disappeared",
			opener: func(*os.File, string, string, fileidentity.ObjectType) (*os.File, bool, error) {
				return nil, false, nil
			},
			want: errLegacyIntegrity,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent, childPath := newParent(t)
			identity, objectType, exists, err := fileidentity.ExistingWithType(childPath)
			if err != nil || !exists {
				t.Fatal(err)
			}
			lookup := func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
				return identity, objectType, true, nil
			}
			var injected *os.File
			opener := func(
				parentFile *os.File,
				path string,
				name string,
				typeName fileidentity.ObjectType,
			) (*os.File, bool, error) {
				file, opened, openErr := test.opener(parentFile, path, name, typeName)
				injected = file
				if file != nil {
					t.Cleanup(func() { _ = file.Close() })
				}
				return file, opened, openErr
			}
			child, childErr := openPinnedLegacyChildWith(parent, "child.json", lookup, opener)
			if injected != nil {
				assertLegacyHandleClosed(t, injected)
			}
			if child != nil || !errors.Is(childErr, test.want) {
				t.Fatalf("child open fault = %#v, %v", child, childErr)
			}
		})
	}
}

func assertLegacyHandleClosed(t *testing.T, file *os.File) {
	t.Helper()
	if file == nil {
		t.Fatal("injected legacy handle is nil")
	}
	if _, err := file.Stat(); err == nil {
		_ = file.Close()
		t.Fatal("injected legacy handle remained open")
	}
}

func TestLegacyPinnedBindingAndClassificationBoundaries(t *testing.T) {
	if file, err := bindPinnedLegacyFile(nil, "missing", fileidentity.Identity{}, 0); file != nil ||
		!errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("nil bind = %#v, %v", file, err)
	}
	var nilPinned *pinnedLegacyFile
	if err := nilPinned.revalidateNamed(); !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("nil revalidation = %v", err)
	}
	if err := nilPinned.Close(); err != nil {
		t.Fatalf("nil close = %v", err)
	}
	if err := (&pinnedLegacyFile{}).Close(); err != nil {
		t.Fatalf("empty close = %v", err)
	}
	if err := classifyLegacyIdentityError(nil); err != nil {
		t.Fatalf("nil identity error = %v", err)
	}
	if err := classifyLegacyIdentityError(fileidentity.ErrInvalidPath); !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("invalid identity error = %v", err)
	}
	canary := errors.New("identity canary")
	if err := classifyLegacyIdentityError(canary); !errors.Is(err, canary) ||
		errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("operational identity error = %v", err)
	}
	if err := classifyLegacyPathIdentityError(
		filepath.Join(t.TempDir(), "missing"), fileidentity.ErrUnsafeType,
	); !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("missing unsafe identity = %v", err)
	}

	path := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(path, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	pinned, exists, err := openPinnedLegacyPath(path)
	if err != nil || !exists {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		_ = pinned.Close()
		t.Fatal(err)
	}
	if err := pinned.revalidateNamed(); !errors.Is(err, errLegacyIntegrity) {
		_ = pinned.Close()
		t.Fatalf("removed named path = %v", err)
	}
	if err := pinned.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pinned.Close(); err != nil {
		t.Fatalf("repeat close = %v", err)
	}

	t.Run("absolute path failure", func(t *testing.T) {
		original, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(original) })
		removed := t.TempDir()
		if err := os.Chdir(removed); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(removed); err != nil {
			t.Fatal(err)
		}
		if file, exists, err := openPinnedLegacyPath("relative.json"); file != nil ||
			exists || err == nil {
			t.Fatalf("removed cwd path = %#v, %t, %v", file, exists, err)
		}
	})

	t.Run("binding revalidation failure", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "legacy.json")
		if err := os.WriteFile(path, []byte("legacy"), 0o600); err != nil {
			t.Fatal(err)
		}
		identity, objectType, exists, err := fileidentity.ExistingWithType(path)
		if err != nil || !exists {
			t.Fatal(err)
		}
		opened, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if renameErr := os.Rename(path, path+".moved"); renameErr != nil {
			_ = opened.Close()
			t.Fatal(renameErr)
		}
		bound, err := bindPinnedLegacyFile(opened, path, identity, objectType)
		if bound != nil || !errors.Is(err, errLegacyIntegrity) {
			_ = opened.Close()
			t.Fatalf("renamed binding = %#v, %v", bound, err)
		}
		if err := opened.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("retained identity mismatch", func(t *testing.T) {
		root := t.TempDir()
		firstPath := filepath.Join(root, "first")
		secondPath := filepath.Join(root, "second")
		for _, candidate := range []string{firstPath, secondPath} {
			if err := os.WriteFile(candidate, []byte("legacy"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		opened, err := os.Open(firstPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = opened.Close() })
		secondIdentity, _, exists, err := fileidentity.ExistingWithType(secondPath)
		if err != nil || !exists {
			t.Fatal(err)
		}
		mismatched := &pinnedLegacyFile{
			file: opened, path: firstPath, identity: secondIdentity,
			objectType: fileidentity.ObjectTypeRegular,
		}
		if err := mismatched.revalidateNamed(); !errors.Is(err, errLegacyIntegrity) {
			t.Fatalf("mismatched retained identity = %v", err)
		}
	})
}

func TestLegacyUnixOpenFaultBoundaries(t *testing.T) {
	if file, exists, err := openLegacyChildNoFollow(
		nil, "missing", "missing", fileidentity.ObjectTypeRegular,
	); file != nil || exists || !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("nil parent open = %#v, %t, %v", file, exists, err)
	}
	if directory, err := openLegacyUnixDirectory(string(os.PathSeparator)); err != nil {
		t.Fatal(err)
	} else if closeErr := directory.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if directory, err := openLegacyUnixDirectory(
		filepath.Join(string(os.PathSeparator), strings.Repeat("x", legacyComponentMaxBytes+1)),
	); directory != nil || !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("invalid ancestor component = %#v, %v", directory, err)
	}
	if file, exists, err := openLegacyUnixAt(
		-1, "child", "child", fileidentity.ObjectTypeRegular,
	); file != nil || exists || err == nil || errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("bad parent fd = %#v, %t, %v", file, exists, err)
	}
	symlinkRoot := t.TempDir()
	target := filepath.Join(symlinkRoot, "target")
	alias := filepath.Join(symlinkRoot, "alias")
	if err := os.WriteFile(target, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err == nil {
		if file, exists, err := openLegacyNoFollow(
			alias, fileidentity.ObjectTypeRegular,
		); file != nil || exists || !errors.Is(err, errLegacyIntegrity) {
			t.Fatalf("terminal symlink open = %#v, %t, %v", file, exists, err)
		}
	}
	parent, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	if file, exists, err := openLegacyUnixAt(
		int(parent.Fd()), "missing-child", "missing-child", fileidentity.ObjectTypeRegular,
	); file != nil || exists || err != nil {
		t.Fatalf("missing child = %#v, %t, %v", file, exists, err)
	}
}

func TestLegacyCoverageRejectsRelativePathsFromRemovedWorkingDirectory(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	filePath := filepath.Join(t.TempDir(), "legacy.json")
	if writeErr := os.WriteFile(filePath, []byte("legacy"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	opened, err := os.Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	identity, objectType, err := fileidentity.Opened(opened)
	if err != nil {
		t.Fatal(err)
	}
	removed := t.TempDir()
	if err := os.Chdir(removed); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	if _, err := generationExclusionsWithOps(
		[]storecatalog.Spec{{ID: "global/auth", Path: "relative.db"}},
		generationExclusionOps{lstat: os.Lstat, identity: fileidentity.Existing},
	); err == nil {
		t.Fatal("generation exclusion resolved a path from a removed working directory")
	}
	pinned := &pinnedLegacyFile{
		file: opened, path: "relative.json", identity: identity, objectType: objectType,
	}
	if excluded, err := excludedPinnedLegacyFile(pinned, generationSet{}); excluded || err == nil {
		t.Fatalf("relative pinned exclusion from removed cwd = %t, %v", excluded, err)
	}
}
