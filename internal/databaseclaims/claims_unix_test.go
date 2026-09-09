//go:build unix

//nolint:govet // Independent failure-boundary assertions use narrow error scopes.
package databaseclaims

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestAcquireRejectsUnsafeClaimRootPermissions(t *testing.T) {
	cache := productionHierarchyTestRoot(t)
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
	cache := productionHierarchyTestRoot(t)
	root := filepath.Join(cache, claimApplicationDirectory, claimDirectoryName)
	if err := preparePlatformClaimRoot(cache, root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("created claim root = %#v, %v", info, err)
	}
}

func TestPreparePlatformClaimRootRejectsIntermediateSymlink(t *testing.T) {
	cache := productionHierarchyTestRoot(t)
	target := productionHierarchyTestRoot(t)
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
	cache := productionHierarchyTestRoot(t)
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
		testClaims := newTestClaimAcquirer(t)
		home := isolatedTestClaimRoot(t)
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
		root := testClaims.root
		path := filepath.Join(root, claims[0].String()+".lock")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if lease, acquireErr := testClaims.Acquire(options, fence); lease != nil ||
			database.CodeOf(acquireErr) != database.CodeIntegrity {
			t.Fatalf("Acquire() with permissive lock = %#v, %v", lease, acquireErr)
		}
	})

	t.Run("strict build failure", func(t *testing.T) {
		testClaims := newTestClaimAcquirer(t)
		home := isolatedTestClaimRoot(t)
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
		if lease, acquireErr := testClaims.Acquire(options, fence); lease != nil || acquireErr == nil {
			t.Fatalf("Acquire() with unsafe generation = %#v, %v", lease, acquireErr)
		}
		if err := os.Remove(unsafeGeneration); err != nil {
			t.Fatal(err)
		}
		lease, err := testClaims.Acquire(options, fence)
		if err != nil {
			t.Fatalf("Acquire() after strict build failure = %v", err)
		}
		_ = lease.Close()
	})
}

func TestLeasePoisonsWhenClaimLockPathIsReplaced(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := isolatedTestClaimRoot(t)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.Acquire(testOptions(t, home, &config.Config{}), fence)
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
	root := isolatedTestClaimRoot(t)
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
	testClaims := newTestClaimAcquirer(t)
	home := isolatedTestClaimRoot(t)
	target := filepath.Join(isolatedTestClaimRoot(t), "target.db")
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
	if lease, err := testClaims.Acquire(testOptions(t, home, &config.Config{}), fence); lease != nil ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("Acquire(symlinked member) = %#v, %v", lease, err)
	}
}

func TestUnixClaimLockRejectsSymlinkAndDirectory(t *testing.T) {
	root := isolatedTestClaimRoot(t)
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkIdentity := strings.Repeat("a", 64)
	if err := os.Symlink(target, filepath.Join(root, symlinkIdentity+".lock")); err != nil {
		t.Fatal(err)
	}
	if claim, err := acquireClaimForTesting(root, symlinkIdentity); claim != nil ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("acquireClaim(symlink) = %#v, %v", claim, err)
	}
	directoryIdentity := strings.Repeat("b", 64)
	if err := os.Mkdir(filepath.Join(root, directoryIdentity+".lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if claim, err := acquireClaimForTesting(root, directoryIdentity); claim != nil || err == nil {
		t.Fatalf("acquireClaim(directory) = %#v, %v", claim, err)
	}
	var nilClaim *unixClaimHandle
	if err := nilClaim.close(); err != nil {
		t.Fatalf("nil claim close = %v", err)
	}
	if err := (&unixClaimHandle{}).close(); err != nil {
		t.Fatalf("empty claim close = %v", err)
	}
	if nilClaim.valid() || (&unixClaimHandle{}).valid() {
		t.Fatal("claim without open lock reported valid")
	}
}

func TestUnixClaimLockReportsContentionAndCreationFailure(t *testing.T) {
	root := isolatedTestClaimRoot(t)
	identity := strings.Repeat("c", 64)
	first, err := acquireClaimForTesting(root, identity)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := acquireClaimForTesting(root, identity); second != nil || !errors.Is(err, errClaimBusy) {
		t.Fatalf("contending acquireClaim() = %#v, %v", second, err)
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}
	second, err := acquireClaimForTesting(root, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.close(); err != nil {
		t.Fatal(err)
	}

	if os.Geteuid() == 0 {
		return
	}
	blocked := isolatedTestClaimRoot(t)
	if err := os.Chmod(blocked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	if claim, err := acquireClaimForTesting(blocked, strings.Repeat("d", 64)); claim != nil || err == nil {
		t.Fatalf("acquireClaim(non-writable root) = %#v, %v", claim, err)
	}
}

func TestUnixClaimCreationBoundaryRejectsUnsafeAncestors(t *testing.T) {
	t.Run("permission denied", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory search permissions")
		}
		blocked := filepath.Join(productionHierarchyTestRoot(t), "blocked")
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
		path := filepath.Join(productionHierarchyTestRoot(t), "file")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateClaimCreationBoundary(path); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("regular-file boundary = %v", err)
		}
	})

	t.Run("writable directory", func(t *testing.T) {
		path := productionHierarchyTestRoot(t)
		if err := os.Chmod(path, 0o770); err != nil {
			t.Fatal(err)
		}
		if err := validateClaimCreationBoundary(
			filepath.Join(path, "child"),
		); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("writable boundary = %v", err)
		}
	})

	t.Run("resolved alias", func(t *testing.T) {
		root := productionHierarchyTestRoot(t)
		target := filepath.Join(root, "target")
		if err := os.MkdirAll(filepath.Join(target, "child"), 0o700); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(root, "alias")
		if err := os.Symlink(target, alias); err != nil {
			t.Fatal(err)
		}
		if err := validateClaimCreationBoundary(
			filepath.Join(alias, "child"),
		); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("aliased boundary = %v", err)
		}
	})

	if os.Geteuid() != 0 {
		if err := validateClaimCreationBoundary(
			string(os.PathSeparator),
		); database.CodeOf(err) != database.CodeUnauthorized {
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
	missing := filepath.Join(isolatedTestClaimRoot(t), "missing")
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

type mutatingReplacementHandle struct {
	delegate    replacementHandle
	mutate      func()
	mutateMatch int
	matchesSeen int
	mutateClose bool
}

func (handle *mutatingReplacementHandle) valid() bool {
	return handle != nil && handle.delegate != nil && handle.delegate.valid()
}

func (handle *mutatingReplacementHandle) matches(identity fileidentity.Identity) bool {
	handle.matchesSeen++
	if handle.matchesSeen == handle.mutateMatch && handle.mutate != nil {
		handle.mutate()
	}
	return handle.delegate.matches(identity)
}

func (handle *mutatingReplacementHandle) close() error {
	if handle.mutateClose && handle.mutate != nil {
		handle.mutate()
	}
	return handle.delegate.close()
}

func (info coverageClaimRootInfo) Name() string       { return "claim-root" }
func (info coverageClaimRootInfo) Size() int64        { return 0 }
func (info coverageClaimRootInfo) Mode() os.FileMode  { return info.mode }
func (info coverageClaimRootInfo) ModTime() time.Time { return time.Time{} }
func (info coverageClaimRootInfo) IsDir() bool        { return info.mode.IsDir() }
func (info coverageClaimRootInfo) Sys() any           { return info.sys }

func TestUnixPlatformClaimRootTransitionFaults(t *testing.T) {
	root := productionHierarchyTestRoot(t)
	canary := errors.New("platform root canary")
	if err := preparePlatformClaimRootWithOps(
		root, platformClaimRootOps{},
	); database.CodeOf(err) != database.CodeIntegrity {
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

func TestUnixClaimRootCreationFailsBeforeUnsafeMutation(t *testing.T) {
	t.Run("unsafe creation boundary", func(t *testing.T) {
		ancestor := productionHierarchyTestRoot(t)
		if err := os.Chmod(ancestor, 0o770); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(ancestor, "new", "claim-root")
		if err := createClaimRootNoFollow(root); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("createClaimRootNoFollow(unsafe boundary) = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(ancestor, "new")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsafe boundary was mutated: %v", err)
		}
		if err := preparePlatformClaimRootWithOps(
			root, defaultPlatformClaimRootOps(),
		); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("preparePlatformClaimRootWithOps(unsafe boundary) = %v", err)
		}
	})

	t.Run("root inspection error", func(t *testing.T) {
		root := filepath.Join(productionHierarchyTestRoot(t), strings.Repeat("x", 4096))
		if err := createClaimRootNoFollow(root); err == nil {
			t.Fatal("createClaimRootNoFollow accepted overlong root")
		}
	})

	t.Run("mkdir failure", func(t *testing.T) {
		root := filepath.Join(productionHierarchyTestRoot(t), "claim-root")
		canary := errors.New("mkdir canary")
		ops := defaultPlatformClaimRootOps()
		ops.mkdirAll = func(string, os.FileMode) error { return canary }
		if err := preparePlatformClaimRootWithOps(root, ops); !errors.Is(err, canary) {
			t.Fatalf("preparePlatformClaimRootWithOps(mkdir failure) = %v", err)
		}
	})
}

func TestUnixClaimRootRejectsUnsafeAncestorAbovePrivateLeaf(t *testing.T) {
	trusted := productionHierarchyTestRoot(t)
	unsafeParent := filepath.Join(trusted, "unsafe-parent")
	root := filepath.Join(unsafeParent, "private-root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafeParent, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := createClaimRootNoFollow(root); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("private root below mutable ancestor = %v", err)
	}
}

func TestLeaseRejectsCatalogAncestorChangedToSymlink(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := isolatedTestClaimRoot(t)
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.Acquire(testOptions(t, home, &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
	}), fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	realWorkspace := workspace + "-real"
	if err := os.Rename(workspace, realWorkspace); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realWorkspace, workspace); err != nil {
		t.Fatal(err)
	}
	if err := lease.Refresh(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("Refresh after ancestor alias = %v", err)
	}
}

func TestPinReplacementRejectsAliasedAncestor(t *testing.T) {
	lease := acquireMigrationLease(t)
	realDirectory := filepath.Join(lease.home, "real-stage")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(realDirectory, "stage.db")
	if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(lease.home, "alias-stage")
	if err := os.Symlink(realDirectory, alias); err != nil {
		t.Fatal(err)
	}
	if err := lease.PinReplacement(
		"global/auth", filepath.Join(alias, "stage.db"),
	); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("PinReplacement(aliased ancestor) = %v", err)
	}
}

func TestReplacementHandleRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(isolatedTestClaimRoot(t), "stage-fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		handle, err := openReplacementHandle(path)
		if handle != nil {
			_ = handle.close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO replacement handle was accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("opening FIFO replacement stage blocked")
	}
}

func TestLeaseFenceLossRemainsPoisonedAfterBoundaryRestored(t *testing.T) {
	testClaims := newTestClaimAcquirer(t)
	home := isolatedTestClaimRoot(t)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.Acquire(testOptions(t, home, &config.Config{}), fence)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	state := filepath.Join(home, database.StateDirectoryName)
	if err := os.Chmod(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("Check after fence loss = %v", err)
	}
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if !fence.Authorizes(home) {
		t.Fatal("fence did not recover after restoring its boundary")
	}
	if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("poisoned lease revived after fence recovery: %v", err)
	}
}

func TestRefreshCheckedGuardRejectsBoundaryDriftDuringObservation(t *testing.T) {
	lease := acquireMigrationLease(t)
	ops := defaultClaimRefreshOps()
	originalObserve := ops.observe
	entered := make(chan struct{})
	releaseObservation := make(chan struct{})
	var once sync.Once
	ops.observe = func(catalog *storecatalog.Catalog) ([]memberObservation, error) {
		once.Do(func() {
			close(entered)
			<-releaseObservation
		})
		return originalObserve(catalog)
	}
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- lease.refreshWithOps("", ops) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Refresh did not enter guarded observation")
	}
	state := filepath.Join(lease.home, database.StateDirectoryName)
	if err := os.Chmod(state, 0o755); err != nil {
		close(releaseObservation)
		t.Fatal(err)
	}
	close(releaseObservation)
	select {
	case err := <-refreshDone:
		if database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("Refresh with drifted guarded fence = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Refresh hung while validating drifted guarded fence")
	}
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if !lease.fence.Authorizes(lease.home) {
		t.Fatal("fence did not recover after restoring boundary")
	}
	if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("lease revived after guarded fence drift: %v", err)
	}
}

func TestPinCheckedGuardRejectsBoundaryDriftAtFinalBoundaries(t *testing.T) {
	for _, test := range []struct {
		name        string
		mutateMatch int
		mutateClose bool
	}{
		{name: "during final pin proof", mutateMatch: 4},
		{name: "after prior pin close", mutateClose: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			lease := acquireMigrationLease(t)
			first := writeReplacementStage(t, lease.home, "guarded-first.db")
			second := writeReplacementStage(t, lease.home, "guarded-second.db")
			if err := lease.PinReplacement("global/auth", first); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(lease.home, database.StateDirectoryName)
			pin := lease.replacementPins["global/auth"]
			pin.handle = &mutatingReplacementHandle{
				delegate: pin.handle, mutateMatch: test.mutateMatch, mutateClose: test.mutateClose,
				mutate: func() { _ = os.Chmod(state, 0o755) },
			}
			if err := lease.PinReplacement("global/auth", second); database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("PinReplacement with boundary drift = %v", err)
			}
			if err := os.Chmod(state, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := lease.Check(); database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("pin boundary drift did not poison lease: %v", err)
			}
		})
	}
}

func TestClaimLeaseFileDescriptorsStayBounded(t *testing.T) {
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("descriptor inventory unavailable: %v", err)
	}
	testClaims := newTestClaimAcquirer(t)
	home := isolatedTestClaimRoot(t)
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	lease, err := testClaims.Acquire(testOptions(t, home, &config.Config{}), fence)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	// One descriptor per retained claim plus a small, fixed fence allowance;
	// root hierarchy descriptors must not be retained per claim.
	if growth := len(after) - len(before); growth > len(lease.claims)+8 {
		t.Fatalf("descriptor growth=%d claims=%d", growth, len(lease.claims))
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUnixReplacementHandleFailureBoundaries(t *testing.T) {
	root := isolatedTestClaimRoot(t)
	if handle, err := openReplacementHandle(filepath.Join(root, "missing")); handle != nil || err == nil {
		t.Fatalf("openReplacementHandle(missing) = %#v, %v", handle, err)
	}
	if handle, err := openReplacementHandle(root); handle != nil || database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("openReplacementHandle(directory) = %#v, %v", handle, err)
	}
	var absent *unixReplacementHandle
	if absent.valid() || absent.matches(fileidentity.Identity{}) || absent.close() != nil {
		t.Fatal("nil replacement handle reported authority")
	}
	path := filepath.Join(root, "stage.db")
	if err := os.WriteFile(path, []byte("stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	generic, err := openReplacementHandle(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, ok := generic.(*unixReplacementHandle)
	if !ok {
		t.Fatalf("replacement handle type = %T", generic)
	}
	if handle.matches(fileidentity.Identity{}) {
		t.Fatal("replacement handle matched invalid identity")
	}
	if err := handle.file.Close(); err != nil {
		t.Fatal(err)
	}
	if handle.valid() {
		t.Fatal("closed replacement handle remained valid")
	}
	handle.file = nil
	if err := handle.close(); err != nil {
		t.Fatalf("empty replacement close = %v", err)
	}
}

func TestUnixClaimValidationHelperBoundaries(t *testing.T) {
	if _, err := walkUnixClaimHierarchy("relative", false, false); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("relative hierarchy = %v", err)
	}
	if err := validateClaimCreationBoundary("relative"); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("relative creation boundary = %v", err)
	}
	if err := validateUnixClaimDirectory(nil, false); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("nil directory stat = %v", err)
	}
	if stat, ok := infoSyscallStat(nil); stat != nil || ok {
		t.Fatalf("infoSyscallStat(nil) = %#v, %t", stat, ok)
	}
	if root, err := PrepareRootForTesting(filepath.Join(t.TempDir(), "missing")); root != "" ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("PrepareRootForTesting(missing) = %q, %v", root, err)
	}
	regularRoot := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(regularRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if root, err := PrepareRootForTesting(regularRoot); root != "" ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("PrepareRootForTesting(regular) = %q, %v", root, err)
	}
	foreign := &unix.Stat_t{Mode: unix.S_IFDIR | 0o700, Uid: uint32(os.Geteuid() + 1)}
	if err := validateUnixClaimDirectory(foreign, false); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("foreign directory stat = %v", err)
	}
	privateRoot := &unix.Stat_t{Mode: unix.S_IFDIR | 0o755, Uid: uint32(os.Geteuid())}
	if err := validateUnixClaimDirectory(privateRoot, true); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("non-private leaf stat = %v", err)
	}

	root := productionHierarchyTestRoot(t)
	ops := defaultPlatformClaimRootOps()
	ops.lstat = func(string) (os.FileInfo, error) {
		return coverageClaimRootInfo{
			mode: os.ModeDir | 0o755,
			sys:  &syscall.Stat_t{Uid: uint32(os.Geteuid())},
		}, nil
	}
	if err := preparePlatformClaimRootWithOps(root, ops); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("permissive root from operations = %v", err)
	}

	unclean := root + string(os.PathSeparator) + ".." + string(os.PathSeparator) + filepath.Base(root)
	for _, path := range []string{"relative", unclean} {
		if _, err := validateExplicitTestClaimRoot(path); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("validateExplicitTestClaimRoot(%q) = %v", path, err)
		}
	}
	alias := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := validateExplicitTestClaimRoot(alias); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("aliased explicit root = %v", err)
	}
	permissive := filepath.Join(root, "permissive")
	if err := os.Mkdir(permissive, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := validateExplicitTestClaimRoot(permissive); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("permissive explicit root = %v", err)
	}
}

func TestPrepareTestClaimRootFailsFromRemovedWorkingDirectory(t *testing.T) {
	removed := filepath.Join(t.TempDir(), "removed-working-directory")
	if err := os.Mkdir(removed, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(removed)
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	if root, err := PrepareRootForTesting("relative-root"); root != "" ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("PrepareRootForTesting from removed cwd = %q, %v", root, err)
	}
}

func TestUnixClaimHandleRejectsIncompleteStateAndUnsafeRoot(t *testing.T) {
	root := isolatedTestClaimRoot(t)
	generic, err := acquireClaimForTesting(root, strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	handle, ok := generic.(*unixClaimHandle)
	if !ok {
		t.Fatalf("claim handle type = %T", generic)
	}
	missingRoot := *handle
	missingRoot.rootPath = ""
	if missingRoot.valid() {
		t.Fatal("claim with missing root state remained valid")
	}
	wrongIdentity := *handle
	wrongIdentity.rootIdentity.inode++
	if wrongIdentity.valid() {
		t.Fatal("claim with wrong root identity remained valid")
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if handle.valid() {
		t.Fatal("claim below permissive root remained valid")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := handle.close(); err != nil {
		t.Fatal(err)
	}
}
