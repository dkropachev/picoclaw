//go:build unix && !aix

package sqlitestore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

type sealedAbsentCountdownContext struct {
	context.Context
	remaining int
}

func (ctx *sealedAbsentCountdownContext) Err() error {
	ctx.remaining--
	if ctx.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

type sealedAbsentActionContext struct {
	context.Context
	remaining int
	action    func()
}

func (ctx *sealedAbsentActionContext) Err() error {
	ctx.remaining--
	if ctx.remaining == 0 && ctx.action != nil {
		ctx.action()
		ctx.action = nil
	}
	return nil
}

func captureSealedAbsentUnixTestProof(
	t *testing.T,
) (string, string, []string, *sealedAbsentLegacyRootPlatform) {
	t.Helper()
	ancestor := privateSealedAbsentLegacyTestDirectory(t)
	path := filepath.Join(ancestor, "missing-parent", "legacy")
	platform, ancestorPath, suffix, err := captureSealedAbsentLegacyRootPlatform(t.Context(), path)
	if err != nil {
		t.Fatalf("capture Unix proof: %v", err)
	}
	t.Cleanup(func() { _ = closeSealedAbsentLegacyRootPlatform(platform) })
	return path, ancestorPath, suffix, platform
}

func TestSealedAbsentUnixCaptureFaultCoverage(t *testing.T) {
	canary := errors.New("sealed absent Unix capture canary")
	if platform, _, _, err := captureSealedAbsentLegacyRootPlatform(
		t.Context(),
		string(os.PathSeparator),
	); platform != nil || err == nil {
		t.Fatalf("filesystem-root capture = %#v, %v", platform, err)
	}

	t.Run("filesystem root open", func(t *testing.T) {
		swapTestHook(t, &sealedAbsentUnixOpen, func(string, int, uint32) (int, error) {
			return -1, canary
		})
		if platform, _, _, err := captureSealedAbsentLegacyRootPlatform(
			t.Context(), filepath.Join(string(os.PathSeparator), "missing"),
		); platform != nil || !errors.Is(err, canary) {
			t.Fatalf("root-open capture = %#v, %v", platform, err)
		}
	})

	t.Run("canceled traversal", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if platform, _, _, err := captureSealedAbsentLegacyRootPlatform(
			ctx, filepath.Join(string(os.PathSeparator), "missing"),
		); platform != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled capture = %#v, %v", platform, err)
		}
	})

	t.Run("traversal close", func(t *testing.T) {
		path := filepath.Join(privateSealedAbsentLegacyTestDirectory(t), "missing")
		original := sealedAbsentUnixFileClose
		calls := 0
		swapTestHook(t, &sealedAbsentUnixFileClose, func(file *os.File) error {
			calls++
			err := original(file)
			if calls == 1 {
				return errors.Join(canary, err)
			}
			return err
		})
		platform, _, _, err := captureSealedAbsentLegacyRootPlatform(t.Context(), path)
		if platform != nil || !errors.Is(err, canary) {
			t.Fatalf("traversal-close capture = %#v, %v", platform, err)
		}
	})
}

func TestSealedAbsentUnixOpenHelperFaultCoverage(t *testing.T) {
	canary := errors.New("sealed absent Unix helper canary")

	t.Run("root new file", func(t *testing.T) {
		swapTestHook(t, &sealedAbsentUnixNewFile, func(uintptr, string) *os.File { return nil })
		if file, err := openSealedAbsentUnixFilesystemRoot(); file != nil || err == nil {
			t.Fatalf("nil root file = %#v, %v", file, err)
		}
	})

	if file, err := openSealedAbsentUnixDirectoryAt(nil, "missing"); file != nil || err == nil {
		t.Fatalf("nil parent directory lookup = %#v, %v", file, err)
	}

	parentPath := privateSealedAbsentLegacyTestDirectory(t)
	if err := os.Mkdir(filepath.Join(parentPath, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()

	t.Run("directory new file", func(t *testing.T) {
		swapTestHook(t, &sealedAbsentUnixNewFile, func(uintptr, string) *os.File { return nil })
		if file, err := openSealedAbsentUnixDirectoryAt(parent, "child"); file != nil || err == nil {
			t.Fatalf("nil child file = %#v, %v", file, err)
		}
	})

	t.Run("directory identity", func(t *testing.T) {
		swapTestHook(t, &sealedAbsentUnixFileOpened, func(*os.File) (
			fileidentity.Identity,
			fileidentity.ObjectType,
			error,
		) {
			return fileidentity.Identity{}, fileidentity.ObjectType(0), canary
		})
		if file, err := openSealedAbsentUnixDirectoryAt(parent, "child"); file != nil || !errors.Is(err, canary) {
			t.Fatalf("child identity fault = %#v, %v", file, err)
		}
	})

	if _, err := sealedAbsentUnixTrustedDirectoryIdentity(nil); err == nil {
		t.Fatal("nil trusted ancestor succeeded")
	}
}

func TestSealedAbsentUnixRevalidationFaultCoverage(t *testing.T) {
	if err := revalidateSealedAbsentLegacyRootPlatform(nil, "", "", nil, nil); err == nil {
		t.Fatal("invalid Unix proof revalidated")
	}

	t.Run("retained identity", func(t *testing.T) {
		_, ancestorPath, suffix, platform := captureSealedAbsentUnixTestProof(t)
		_, _, _, other := captureSealedAbsentUnixTestProof(t)
		platform.identity = other.identity
		if err := revalidateSealedAbsentLegacyRootPlatform(
			t.Context(), "", ancestorPath, suffix, platform,
		); err == nil {
			t.Fatal("wrong retained identity revalidated")
		}
	})

	for checkpoint := 1; checkpoint <= 40; checkpoint++ {
		t.Run(fmt.Sprintf("cancellation checkpoint %d", checkpoint), func(t *testing.T) {
			_, ancestorPath, suffix, platform := captureSealedAbsentUnixTestProof(t)
			ctx := &sealedAbsentCountdownContext{
				Context:   context.Background(),
				remaining: checkpoint,
			}
			_ = revalidateSealedAbsentLegacyRootPlatform(ctx, "", ancestorPath, suffix, platform)
		})
	}

	t.Run("named ancestor", func(t *testing.T) {
		_, ancestorPath, suffix, platform := captureSealedAbsentUnixTestProof(t)
		err := revalidateSealedAbsentLegacyRootPlatform(
			t.Context(), "", filepath.Join(ancestorPath, "not-present"), suffix, platform,
		)
		if err == nil {
			t.Fatal("missing named ancestor revalidated")
		}
	})

	t.Run("missing name appears", func(t *testing.T) {
		_, ancestorPath, suffix, platform := captureSealedAbsentUnixTestProof(t)
		if err := os.Mkdir(filepath.Join(ancestorPath, suffix[0]), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := revalidateSealedAbsentLegacyRootPlatform(
			t.Context(), "", ancestorPath, suffix, platform,
		); err == nil {
			t.Fatal("appeared name revalidated")
		}
	})

	for checkpoint := 1; checkpoint <= 30; checkpoint++ {
		t.Run(fmt.Sprintf("ancestor replacement checkpoint %d", checkpoint), func(t *testing.T) {
			_, ancestorPath, suffix, platform := captureSealedAbsentUnixTestProof(t)
			moved := ancestorPath + ".moved"
			t.Cleanup(func() { _ = os.RemoveAll(moved) })
			ctx := &sealedAbsentActionContext{
				Context:   context.Background(),
				remaining: checkpoint,
				action: func() {
					if err := os.Rename(ancestorPath, moved); err != nil {
						t.Fatalf("rename ancestor: %v", err)
					}
					if err := os.Mkdir(ancestorPath, 0o700); err != nil {
						t.Fatalf("replace ancestor: %v", err)
					}
				},
			}
			_ = revalidateSealedAbsentLegacyRootPlatform(ctx, "", ancestorPath, suffix, platform)
		})
	}
}

func TestSealedAbsentUnixNamedAncestorFaultCoverage(t *testing.T) {
	canary := errors.New("sealed absent Unix named canary")
	_, ancestorPath, _, platform := captureSealedAbsentUnixTestProof(t)

	if err := validateSealedAbsentUnixNamedAncestor(nil, ancestorPath, platform.identity); err == nil {
		t.Fatal("nil named-ancestor context succeeded")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := validateSealedAbsentUnixNamedAncestor(
		canceled,
		ancestorPath,
		platform.identity,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled named-ancestor error = %v", err)
	}

	t.Run("root open", func(t *testing.T) {
		swapTestHook(t, &sealedAbsentUnixOpen, func(string, int, uint32) (int, error) {
			return -1, canary
		})
		if err := validateSealedAbsentUnixNamedAncestor(
			t.Context(), ancestorPath, platform.identity,
		); !errors.Is(err, canary) {
			t.Fatalf("named root-open error = %v", err)
		}
	})

	if err := validateSealedAbsentUnixNamedAncestor(
		t.Context(), ancestorPath+string(os.PathSeparator)+"..", platform.identity,
	); err == nil {
		t.Fatal("ambiguous named ancestor succeeded")
	}
	if err := validateSealedAbsentUnixNamedAncestor(
		t.Context(), filepath.Join(ancestorPath, "missing"), platform.identity,
	); err == nil {
		t.Fatal("missing named ancestor succeeded")
	}

	t.Run("path close", func(t *testing.T) {
		original := sealedAbsentUnixFileClose
		calls := 0
		swapTestHook(t, &sealedAbsentUnixFileClose, func(file *os.File) error {
			calls++
			err := original(file)
			if calls == 1 {
				return errors.Join(canary, err)
			}
			return err
		})
		if err := validateSealedAbsentUnixNamedAncestor(
			t.Context(), ancestorPath, platform.identity,
		); !errors.Is(err, canary) {
			t.Fatalf("named path-close error = %v", err)
		}
	})
}

func TestSealedAbsentUnixMissingNameFaultCoverage(t *testing.T) {
	if err := requireSealedAbsentUnixName(nil, "missing"); err == nil {
		t.Fatal("nil missing-name parent succeeded")
	}
	ancestor := privateSealedAbsentLegacyTestDirectory(t)
	parent, err := os.Open(ancestor)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	canary := errors.New("sealed absent fstatat canary")
	swapTestHook(t, &sealedAbsentUnixFstatat, func(int, string, *unix.Stat_t, int) error {
		return canary
	})
	if err := requireSealedAbsentUnixName(parent, "missing"); !errors.Is(err, canary) {
		t.Fatalf("missing-name inspection error = %v", err)
	}
}
