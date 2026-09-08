//go:build unix

package databaseclaims

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestAcquireRejectsUnsafeClaimRootPermissions(t *testing.T) {
	cache := secureTestDir(t)
	root := filepath.Join(cache, claimApplicationDirectory, claimDirectoryName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := preparePlatformClaimRoot(cache, root); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("preparePlatformClaimRoot() = %v", err)
	}
}

func TestPreparePlatformClaimRootCreatesPrivateHierarchy(t *testing.T) {
	cache := secureTestDir(t)
	root := filepath.Join(cache, "nested", claimApplicationDirectory, claimDirectoryName)
	if err := preparePlatformClaimRoot(cache, root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("created claim root = %#v, %v", info, err)
	}
}

func TestPreparePlatformClaimRootRejectsIntermediateSymlink(t *testing.T) {
	cache := secureTestDir(t)
	target := secureTestDir(t)
	alias := filepath.Join(cache, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	root := filepath.Join(alias, claimApplicationDirectory, claimDirectoryName)
	if err := preparePlatformClaimRoot(cache, root); err == nil {
		t.Fatal("claim root creation followed an intermediate symlink")
	}
	if _, err := os.Lstat(filepath.Join(target, claimApplicationDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected claim root mutated symlink target: %v", err)
	}
}

func TestPreparePlatformClaimRootPropagatesCreationAndInspectionFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses ordinary directory creation permissions")
	}
	cache := secureTestDir(t)
	blocked := filepath.Join(cache, "blocked")
	if err := os.Mkdir(blocked, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := preparePlatformClaimRoot(cache, filepath.Join(blocked, "claim-root")); err == nil {
		t.Fatal("claim root creation below non-writable owner directory succeeded")
	}

	overlong := filepath.Join(cache, strings.Repeat("x", 4096))
	if err := validateClaimCreationBoundary(overlong); err == nil {
		t.Fatal("overlong claim boundary succeeded")
	}
	if key, exists, err := physicalClaimKey(overlong); key != "" || exists || err == nil {
		t.Fatalf("physicalClaimKey(overlong) = %q, %t, %v", key, exists, err)
	}
}

func TestAcquireRejectsUnsafeClaimFileAndReleasesAfterBuildFailure(t *testing.T) {
	t.Run("claim file permissions", func(t *testing.T) {
		home := secureTestDir(t)
		fence, err := database.AcquireOnlineFence(home)
		if err != nil {
			t.Fatal(err)
		}
		defer fence.Close()
		options := testOptions(t, home, &config.Config{})
		projected, err := storecatalog.Project(options)
		if err != nil {
			t.Fatal(err)
		}
		claims, err := projected.ClaimIDs()
		if err != nil {
			t.Fatal(err)
		}
		root, err := prepareClaimRoot()
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, claims[0].String()+".lock")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if lease, acquireErr := Acquire(options, fence); lease != nil ||
			database.CodeOf(acquireErr) != database.CodeIntegrity {
			t.Fatalf("Acquire() with permissive lock = %#v, %v", lease, acquireErr)
		}
	})

	t.Run("strict build failure", func(t *testing.T) {
		home := secureTestDir(t)
		fence, err := database.AcquireOnlineFence(home)
		if err != nil {
			t.Fatal(err)
		}
		defer fence.Close()
		options := testOptions(t, home, &config.Config{})
		unsafeGeneration := filepath.Join(home, "auth.db")
		if err := os.Mkdir(unsafeGeneration, 0o700); err != nil {
			t.Fatal(err)
		}
		if lease, acquireErr := Acquire(options, fence); lease != nil || acquireErr == nil {
			t.Fatalf("Acquire() with unsafe generation = %#v, %v", lease, acquireErr)
		}
		if err := os.Remove(unsafeGeneration); err != nil {
			t.Fatal(err)
		}
		lease, err := Acquire(options, fence)
		if err != nil {
			t.Fatalf("Acquire() after strict build failure = %v", err)
		}
		_ = lease.Close()
	})
}

func TestLeasePoisonsWhenClaimLockPathIsReplaced(t *testing.T) {
	home := secureTestDir(t)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := Acquire(testOptions(t, home, &config.Config{}), fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	var handle *unixClaimHandle
	for _, claim := range lease.claims {
		if candidate, ok := claim.(*unixClaimHandle); ok {
			handle = candidate
			break
		}
	}
	if handle == nil || handle.file == nil {
		t.Fatal("lease retained no Unix claim handle")
	}
	path := handle.file.Name()
	old := path + ".identity-test-old"
	if err := os.Rename(path, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(path)
		_ = os.Rename(old, path)
	})
	if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("replaced claim Check = %v", err)
	}
	if lease.Stores() != nil || lease.Home() != "" {
		t.Fatal("replaced claim left lease usable")
	}
}

func TestPhysicalClaimKeyRejectsSymlinkAndIrregularMembers(t *testing.T) {
	root := secureTestDir(t)
	regular := filepath.Join(root, "regular")
	if err := os.WriteFile(regular, []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	if key, exists, err := physicalClaimKey(link); key != "" || exists ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("physicalClaimKey(symlink) = %q, %t, %v", key, exists, err)
	}
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if key, exists, err := physicalClaimKey(fifo); key != "" || exists ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("physicalClaimKey(fifo) = %q, %t, %v", key, exists, err)
	}
}

func TestAcquireRejectsSymlinkedPhysicalMemberBeforeCatalogBuild(t *testing.T) {
	home := secureTestDir(t)
	target := filepath.Join(secureTestDir(t), "target.db")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, "auth.db")); err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	if lease, err := Acquire(testOptions(t, home, &config.Config{}), fence); lease != nil ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("Acquire(symlinked member) = %#v, %v", lease, err)
	}
}

func TestUnixClaimLockRejectsSymlinkAndDirectory(t *testing.T) {
	root := secureTestDir(t)
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkIdentity := strings.Repeat("a", 64)
	if err := os.Symlink(target, filepath.Join(root, symlinkIdentity+".lock")); err != nil {
		t.Fatal(err)
	}
	if claim, err := acquireClaim(root, symlinkIdentity); claim != nil ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("acquireClaim(symlink) = %#v, %v", claim, err)
	}
	directoryIdentity := strings.Repeat("b", 64)
	if err := os.Mkdir(filepath.Join(root, directoryIdentity+".lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if claim, err := acquireClaim(root, directoryIdentity); claim != nil || err == nil {
		t.Fatalf("acquireClaim(directory) = %#v, %v", claim, err)
	}
	var nilClaim *unixClaimHandle
	if err := nilClaim.close(); err != nil {
		t.Fatalf("nil claim close = %v", err)
	}
	if err := (&unixClaimHandle{}).close(); err != nil {
		t.Fatalf("empty claim close = %v", err)
	}
}

func TestUnixClaimLockReportsContentionAndCreationFailure(t *testing.T) {
	root := secureTestDir(t)
	identity := strings.Repeat("c", 64)
	first, err := acquireClaim(root, identity)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := acquireClaim(root, identity); second != nil || !errors.Is(err, errClaimBusy) {
		t.Fatalf("contending acquireClaim() = %#v, %v", second, err)
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}
	second, err := acquireClaim(root, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.close(); err != nil {
		t.Fatal(err)
	}

	if os.Geteuid() == 0 {
		return
	}
	blocked := secureTestDir(t)
	if err := os.Chmod(blocked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	if claim, err := acquireClaim(blocked, strings.Repeat("d", 64)); claim != nil || err == nil {
		t.Fatalf("acquireClaim(non-writable root) = %#v, %v", claim, err)
	}
}

func TestUnixClaimCreationBoundaryRejectsUnsafeAncestors(t *testing.T) {
	t.Run("permission denied", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory search permissions")
		}
		blocked := filepath.Join(secureTestDir(t), "blocked")
		if err := os.Mkdir(blocked, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(blocked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
		if err := validateClaimCreationBoundary(filepath.Join(blocked, "child")); err == nil {
			t.Fatal("permission-denied boundary succeeded")
		}
	})

	t.Run("regular file", func(t *testing.T) {
		path := filepath.Join(secureTestDir(t), "file")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateClaimCreationBoundary(path); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("regular-file boundary = %v", err)
		}
	})

	t.Run("writable directory", func(t *testing.T) {
		path := secureTestDir(t)
		if err := os.Chmod(path, 0o770); err != nil {
			t.Fatal(err)
		}
		if err := validateClaimCreationBoundary(filepath.Join(path, "child")); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("writable boundary = %v", err)
		}
	})

	t.Run("resolved alias", func(t *testing.T) {
		root := secureTestDir(t)
		target := filepath.Join(root, "target")
		if err := os.MkdirAll(filepath.Join(target, "child"), 0o700); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(root, "alias")
		if err := os.Symlink(target, alias); err != nil {
			t.Fatal(err)
		}
		if err := validateClaimCreationBoundary(filepath.Join(alias, "child")); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("aliased boundary = %v", err)
		}
	})

	if os.Geteuid() != 0 {
		if err := validateClaimCreationBoundary(string(os.PathSeparator)); database.CodeOf(err) != database.CodeUnauthorized {
			t.Fatalf("foreign-owner boundary = %v", err)
		}
	}
}

func TestStableClaimCacheRootValidation(t *testing.T) {
	euid := os.Geteuid()
	if root, err := stableClaimCacheRootFor(nil, errors.New("lookup"), euid); root != "" ||
		database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("lookup failure = %q, %v", root, err)
	}
	if root, err := stableClaimCacheRootFor(&user.User{
		Uid: strconv.Itoa(euid), HomeDir: "relative",
	}, nil, euid); root != "" || database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("relative home = %q, %v", root, err)
	}
	missing := filepath.Join(secureTestDir(t), "missing")
	if root, err := stableClaimCacheRootFor(&user.User{
		Uid: strconv.Itoa(euid), HomeDir: missing,
	}, nil, euid); root != "" || database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("missing home = %q, %v", root, err)
	}
}

type coverageClaimRootInfo struct {
	mode os.FileMode
	sys  any
}

func (info coverageClaimRootInfo) Name() string       { return "claim-root" }
func (info coverageClaimRootInfo) Size() int64        { return 0 }
func (info coverageClaimRootInfo) Mode() os.FileMode  { return info.mode }
func (info coverageClaimRootInfo) ModTime() time.Time { return time.Time{} }
func (info coverageClaimRootInfo) IsDir() bool        { return info.mode.IsDir() }
func (info coverageClaimRootInfo) Sys() any           { return info.sys }

func TestUnixPlatformClaimRootTransitionFaults(t *testing.T) {
	root := secureTestDir(t)
	canary := errors.New("platform root canary")
	if err := preparePlatformClaimRootWithOps(root, platformClaimRootOps{}); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("empty platform operations = %v", err)
	}
	validStat := &syscall.Stat_t{Uid: uint32(os.Geteuid())}
	for _, test := range []struct {
		name   string
		mutate func(*platformClaimRootOps)
		code   database.ErrorCode
	}{
		{
			name: "lstat",
			mutate: func(ops *platformClaimRootOps) {
				ops.lstat = func(string) (os.FileInfo, error) { return nil, canary }
			},
		},
		{
			name: "unsafe type", code: database.CodeIntegrity,
			mutate: func(ops *platformClaimRootOps) {
				ops.lstat = func(string) (os.FileInfo, error) {
					return coverageClaimRootInfo{mode: 0o600, sys: validStat}, nil
				}
			},
		},
		{
			name: "missing ownership", code: database.CodeIntegrity,
			mutate: func(ops *platformClaimRootOps) {
				ops.lstat = func(string) (os.FileInfo, error) {
					return coverageClaimRootInfo{mode: os.ModeDir | 0o700}, nil
				}
			},
		},
		{
			name: "foreign ownership", code: database.CodeUnauthorized,
			mutate: func(ops *platformClaimRootOps) {
				ops.lstat = func(string) (os.FileInfo, error) {
					return coverageClaimRootInfo{
						mode: os.ModeDir | 0o700,
						sys:  &syscall.Stat_t{Uid: uint32(os.Geteuid() + 1)},
					}, nil
				}
			},
		},
		{
			name: "canonicalization",
			mutate: func(ops *platformClaimRootOps) {
				ops.evalSymlinks = func(string) (string, error) { return "", canary }
			},
		},
		{
			name: "canonical alias", code: database.CodeIntegrity,
			mutate: func(ops *platformClaimRootOps) {
				ops.evalSymlinks = func(string) (string, error) { return filepath.Dir(root), nil }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := defaultPlatformClaimRootOps()
			test.mutate(&ops)
			err := preparePlatformClaimRootWithOps(root, ops)
			if err == nil || test.code != "" && database.CodeOf(err) != test.code {
				t.Fatalf("preparePlatformClaimRootWithOps() = %v", err)
			}
		})
	}
}
