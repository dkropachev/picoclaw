//go:build unix

package sqliteprovider

import (
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const providerPOSIXLockHelperPath = "PICOCLAW_PROVIDER_POSIX_LOCK_HELPER_PATH"

const providerLegacyRawSQLiteHelperPath = "PICOCLAW_PROVIDER_LEGACY_RAW_SQLITE_HELPER_PATH"

type unixProviderTestFileInfo struct {
	name string
	mode os.FileMode
	stat *syscall.Stat_t
}

func (info unixProviderTestFileInfo) Name() string       { return info.name }
func (info unixProviderTestFileInfo) Size() int64        { return 0 }
func (info unixProviderTestFileInfo) Mode() os.FileMode  { return info.mode }
func (info unixProviderTestFileInfo) ModTime() time.Time { return time.Time{} }
func (info unixProviderTestFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info unixProviderTestFileInfo) Sys() any           { return info.stat }

func newUnixProviderTestFileInfo(name string, inode uint64, links uint64) os.FileInfo {
	stat := &syscall.Stat_t{}
	statValue := reflect.ValueOf(stat).Elem()
	for field, value := range map[string]uint64{
		"Dev": 1, "Ino": inode, "Nlink": links, "Uid": uint64(os.Geteuid()),
	} {
		candidate := statValue.FieldByName(field)
		if candidate.IsValid() && candidate.CanSet() {
			switch candidate.Kind() {
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				candidate.SetUint(value)
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				candidate.SetInt(int64(value))
			}
		}
	}
	return unixProviderTestFileInfo{
		name: name,
		mode: 0o600,
		stat: stat,
	}
}

func TestUnixGenerationLinkCountClassification(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		info os.FileInfo
		want generationLinkClass
	}{
		{name: "missing info", want: generationLinkUnavailable},
		{
			name: "missing Unix stat",
			info: unixProviderTestFileInfo{name: "store.db", mode: 0o600},
			want: generationLinkUnavailable,
		},
		{name: "zero", info: newUnixProviderTestFileInfo("store.db", 1, 0), want: generationLinkZero},
		{name: "single", info: newUnixProviderTestFileInfo("store.db", 1, 1), want: generationLinkSingle},
		{name: "multiple", info: newUnixProviderTestFileInfo("store.db", 1, 2), want: generationLinkMultiple},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyGenerationLinkCount("unused", test.info); got != test.want {
				t.Fatalf("link class = %v, want %v", got, test.want)
			}
		})
	}
}

func TestValidateUnixProviderLiveInfo(t *testing.T) {
	t.Parallel()
	currentUID := uint32(os.Geteuid())
	regular := newUnixProviderTestFileInfo("store.db", 1, 1)
	foreign := newUnixProviderTestFileInfo("store.db", 1, 1).(unixProviderTestFileInfo)
	foreign.stat.Uid = currentUID + 1
	wrongMode := newUnixProviderTestFileInfo("store.db", 1, 1).(unixProviderTestFileInfo)
	wrongMode.mode = 0o640
	nonHardenableMode := newUnixProviderTestFileInfo("store.db", 1, 1).(unixProviderTestFileInfo)
	nonHardenableMode.mode = 0o400
	setuidMode := newUnixProviderTestFileInfo("store.db", 1, 1).(unixProviderTestFileInfo)
	setuidMode.mode = os.ModeSetuid | 0o600
	directory := newUnixProviderTestFileInfo("store.db", 1, 1).(unixProviderTestFileInfo)
	directory.mode = os.ModeDir | 0o700
	symlink := newUnixProviderTestFileInfo("store.db", 1, 1).(unixProviderTestFileInfo)
	symlink.mode = os.ModeSymlink | 0o600

	for _, test := range []struct {
		name           string
		info           os.FileInfo
		wantError      bool
		wantUnsafe     bool
		wantTransition bool
		wantHardening  bool
		wantText       string
	}{
		{name: "valid", info: regular},
		{name: "nil", wantError: true, wantUnsafe: true, wantText: "regular file"},
		{name: "directory", info: directory, wantError: true, wantUnsafe: true, wantText: "regular file"},
		{name: "symlink", info: symlink, wantError: true, wantUnsafe: true, wantText: "regular file"},
		{
			name: "missing stat", info: unixProviderTestFileInfo{name: "store.db", mode: 0o600},
			wantError: true, wantText: "metadata is unavailable",
		},
		{name: "foreign owner", info: foreign, wantError: true, wantUnsafe: true, wantText: "another user"},
		{
			name: "zero links", info: newUnixProviderTestFileInfo("store.db", 1, 0),
			wantError: true, wantTransition: true,
		},
		{
			name: "hardlink", info: newUnixProviderTestFileInfo("store.db", 1, 2),
			wantError: true, wantUnsafe: true, wantText: "hardlink",
		},
		{
			name: "wrong mode", info: wrongMode, wantError: true, wantUnsafe: true,
			wantHardening: true, wantText: "0600",
		},
		{
			name: "non-hardenable mode", info: nonHardenableMode, wantError: true,
			wantUnsafe: true, wantText: "cannot be safely narrowed",
		},
		{name: "setuid mode", info: setuidMode, wantError: true, wantUnsafe: true, wantText: "0600"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateUnixProviderLiveInfo(test.info, currentUID)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError %v", err, test.wantError)
			}
			if errors.Is(err, errProviderUnsafeBoundary) != test.wantUnsafe {
				t.Fatalf("unsafe classification = %v, error %v", errors.Is(err, errProviderUnsafeBoundary), err)
			}
			if errors.Is(err, errProviderGenerationTransition) != test.wantTransition {
				gotTransition := errors.Is(err, errProviderGenerationTransition)
				t.Fatalf(
					"transition classification = %v, error %v",
					gotTransition,
					err,
				)
			}
			if errors.Is(err, errProviderFileModeNeedsHardening) != test.wantHardening {
				t.Fatalf("hardening classification = %v, error %v", test.wantHardening, err)
			}
			if test.wantText != "" && !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("error = %v, want text %q", err, test.wantText)
			}
			if test.wantTransition && strings.Contains(strings.ToLower(err.Error()), "hardlink") {
				t.Fatalf("zero-link transition mislabeled as hardlink: %v", err)
			}
		})
	}
}

func TestUnixProviderHardenableModes(t *testing.T) {
	t.Parallel()
	for _, mode := range []os.FileMode{0o600, 0o604, 0o620, 0o640, 0o660, 0o664, 0o666} {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			if err := validateUnixProviderHardenableMode(mode); err != nil {
				t.Fatalf("mode %04o = %v", mode, err)
			}
			if err := validateUnixProviderHardenableStatMode(uint64(mode)); err != nil {
				t.Fatalf("stat mode %04o = %v", mode, err)
			}
		})
	}
	for _, mode := range []os.FileMode{
		0o000, 0o400, 0o500, 0o601, 0o700, os.ModeSetuid | 0o600,
		os.ModeSetgid | 0o600, os.ModeSticky | 0o600,
	} {
		t.Run("reject "+mode.String(), func(t *testing.T) {
			t.Parallel()
			if err := validateUnixProviderHardenableMode(mode); err == nil ||
				!errors.Is(err, errProviderUnsafeBoundary) {
				t.Fatalf("mode %v error = %v", mode, err)
			}
			statMode := uint32(mode.Perm())
			switch {
			case mode&os.ModeSetuid != 0:
				statMode |= unix.S_ISUID
			case mode&os.ModeSetgid != 0:
				statMode |= unix.S_ISGID
			case mode&os.ModeSticky != 0:
				statMode |= unix.S_ISVTX
			}
			if err := validateUnixProviderHardenableStatMode(uint64(statMode)); err == nil ||
				!errors.Is(err, errProviderUnsafeBoundary) {
				t.Fatalf("stat mode %#o error = %v", statMode, err)
			}
		})
	}
}

func TestUnixProviderNoFollowChmodFallbackClassification(t *testing.T) {
	t.Parallel()
	for _, err := range []error{
		unix.ENOSYS, unix.ENOTSUP, unix.EOPNOTSUPP, unix.EPERM, unix.EACCES, unix.EINVAL,
	} {
		if !unixProviderNoFollowChmodFallback(err) {
			t.Fatalf("fallback error %v was rejected", err)
		}
	}
	for _, err := range []error{nil, unix.EIO, unix.EROFS} {
		if unixProviderNoFollowChmodFallback(err) {
			t.Fatalf("non-fallback error %v was accepted", err)
		}
	}
}

func TestSecureUnixProviderLiveFileFaults(t *testing.T) {
	t.Parallel()
	currentUID := uint32(os.Geteuid())
	root := t.TempDir()
	expectedPath := filepath.Join(root, "expected")
	replacementPath := filepath.Join(root, "replacement")
	if err := os.WriteFile(expectedPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacementPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Lstat(expectedPath)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := os.Lstat(replacementPath)
	if err != nil {
		t.Fatal(err)
	}
	canary := errors.New("live lstat canary")
	chmodCanary := errors.New("live chmod canary")
	if secureErr := secureUnixProviderLiveFile(
		"store.db", expected, nil,
		func(string, os.FileInfo, os.FileMode) error { return nil }, currentUID,
	); secureErr == nil {
		t.Fatal("nil lstat was accepted")
	}
	if secureErr := secureUnixProviderLiveFile(
		"store.db", expected,
		func(string) (os.FileInfo, error) { return expected, nil }, nil, currentUID,
	); secureErr == nil {
		t.Fatal("nil chmod was accepted")
	}

	if secureErr := secureUnixProviderLiveFile(
		"store.db", expected,
		func(string) (os.FileInfo, error) { return expected, nil },
		func(string, os.FileInfo, os.FileMode) error { return nil },
		currentUID,
	); secureErr != nil {
		t.Fatalf("stable live file = %v", secureErr)
	}
	if secureErr := secureUnixProviderLiveFile(
		"store.db", expected,
		func(string) (os.FileInfo, error) { return nil, canary },
		func(string, os.FileInfo, os.FileMode) error { return nil },
		currentUID,
	); !errors.Is(secureErr, canary) {
		t.Fatalf("lstat failure = %v", secureErr)
	}
	if secureErr := secureUnixProviderLiveFile(
		"store.db", expected,
		func(string) (os.FileInfo, error) { return replacement, nil },
		func(string, os.FileInfo, os.FileMode) error { return nil },
		currentUID,
	); !errors.Is(secureErr, errProviderGenerationTransition) {
		t.Fatalf("replacement = %v", secureErr)
	}

	if chmodErr := os.Chmod(expectedPath, 0o640); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	broad, err := os.Lstat(expectedPath)
	if err != nil {
		t.Fatal(err)
	}
	if chmodErr := os.Chmod(expectedPath, 0o600); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	secured, err := os.Lstat(expectedPath)
	if err != nil {
		t.Fatal(err)
	}
	chmodCalls := 0
	if secureErr := secureUnixProviderLiveFile(
		"store.db", broad,
		func(string) (os.FileInfo, error) { return secured, nil },
		func(path string, info os.FileInfo, mode os.FileMode) error {
			chmodCalls++
			if path != "store.db" || !os.SameFile(info, broad) || mode != 0o600 {
				t.Fatalf("chmod inputs = %q, %#v, %04o", path, info, mode)
			}
			return nil
		},
		currentUID,
	); secureErr != nil {
		t.Fatalf("repairable live mode = %v", secureErr)
	}
	if chmodCalls != 1 {
		t.Fatalf("chmod calls = %d, want 1", chmodCalls)
	}
	if secureErr := secureUnixProviderLiveFile(
		"store.db", broad,
		func(string) (os.FileInfo, error) { return broad, nil },
		func(string, os.FileInfo, os.FileMode) error { return chmodCanary },
		currentUID,
	); !errors.Is(secureErr, chmodCanary) {
		t.Fatalf("chmod failure = %v", secureErr)
	}
	if secureErr := secureUnixProviderLiveFile(
		"store.db", broad,
		func(string) (os.FileInfo, error) { return broad, nil },
		func(string, os.FileInfo, os.FileMode) error { return nil },
		currentUID,
	); secureErr == nil || !errors.Is(secureErr, errProviderUnsafeBoundary) ||
		!errors.Is(secureErr, errProviderFileModeNeedsHardening) {
		t.Fatalf("ineffective chmod error = %v", secureErr)
	}
	nonHardenable := newUnixProviderTestFileInfo("store.db", 11, 1).(unixProviderTestFileInfo)
	nonHardenable.mode = 0o400
	chmodCalled := false
	if secureErr := secureUnixProviderLiveFile(
		"store.db", nonHardenable,
		func(string) (os.FileInfo, error) { return nonHardenable, nil },
		func(string, os.FileInfo, os.FileMode) error {
			chmodCalled = true
			return nil
		},
		currentUID,
	); secureErr == nil || !errors.Is(secureErr, errProviderUnsafeBoundary) ||
		!strings.Contains(secureErr.Error(), "cannot be safely narrowed") {
		t.Fatalf("non-hardenable mode error = %v", secureErr)
	}
	if chmodCalled {
		t.Fatal("non-hardenable mode reached chmod")
	}
}

func TestUnixRelativeChmodFallbackPreservesLockAndRejectsReplacement(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	parentInfo, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	parentFD, err := unix.Open(
		root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(parentFD) }()

	t.Run("unsupported no-follow syscall", func(t *testing.T) {
		path := filepath.Join(root, "fallback.db")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
		expected, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 1}
		if lockErr := unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock); lockErr != nil {
			t.Skipf("POSIX record locks unavailable: %v", lockErr)
		}
		defer func() {
			lock.Type = unix.F_UNLCK
			_ = unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock)
		}()

		assertProviderPOSIXLockHeldByParent(t, path)
		var flags []int
		ops := unixProviderAtOps{
			fchmodat: func(fd int, name string, mode uint32, flag int) error {
				flags = append(flags, flag)
				if flag == unix.AT_SYMLINK_NOFOLLOW {
					return unix.EOPNOTSUPP
				}
				return unix.Fchmodat(fd, name, mode, flag)
			},
			fstat:   unix.Fstat,
			fstatat: unix.Fstatat,
		}
		if chmodErr := chmodUnixProviderAtWithOps(
			parentFD, filepath.Base(path), 0o600, parentInfo, expected,
			uint32(os.Geteuid()), ops,
		); chmodErr != nil {
			t.Fatal(chmodErr)
		}
		assertProviderPOSIXLockHeldByParent(t, path)
		if !reflect.DeepEqual(flags, []int{unix.AT_SYMLINK_NOFOLLOW, 0}) {
			t.Fatalf("fchmodat flags = %v", flags)
		}
		secured, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if secured.Mode().Perm() != 0o600 {
			t.Fatalf("fallback mode = %04o", secured.Mode().Perm())
		}
	})

	t.Run("replacement before fallback", func(t *testing.T) {
		path := filepath.Join(root, "replacement.db")
		oldPath := path + ".old"
		target := filepath.Join(root, "target.db")
		for _, candidate := range []string{path, target} {
			if err := os.WriteFile(candidate, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(candidate, 0o640); err != nil {
				t.Fatal(err)
			}
		}
		expected, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		chmodCalls := 0
		ops := unixProviderAtOps{
			fchmodat: func(int, string, uint32, int) error {
				chmodCalls++
				if renameErr := os.Rename(path, oldPath); renameErr != nil {
					t.Fatal(renameErr)
				}
				if symlinkErr := os.Symlink(target, path); symlinkErr != nil {
					t.Fatal(symlinkErr)
				}
				return unix.EOPNOTSUPP
			},
			fstat:   unix.Fstat,
			fstatat: unix.Fstatat,
		}
		if chmodErr := chmodUnixProviderAtWithOps(
			parentFD, filepath.Base(path), 0o600, parentInfo, expected,
			uint32(os.Geteuid()), ops,
		); !errors.Is(chmodErr, errProviderGenerationTransition) {
			t.Fatalf("replacement error = %v", chmodErr)
		}
		if chmodCalls != 1 {
			t.Fatalf("fchmodat calls = %d, want 1", chmodCalls)
		}
		targetInfo, err := os.Lstat(target)
		if err != nil {
			t.Fatal(err)
		}
		if targetInfo.Mode().Perm() != 0o640 {
			t.Fatalf("replacement target mode = %04o", targetInfo.Mode().Perm())
		}
	})

	t.Run("seccomp denied no-follow syscall", func(t *testing.T) {
		path := filepath.Join(root, "seccomp.db")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
		expected, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		var flags []int
		ops := unixProviderAtOps{
			fchmodat: func(fd int, name string, mode uint32, flag int) error {
				flags = append(flags, flag)
				if flag == unix.AT_SYMLINK_NOFOLLOW {
					return unix.EPERM
				}
				return unix.Fchmodat(fd, name, mode, flag)
			},
			fstat:   unix.Fstat,
			fstatat: unix.Fstatat,
		}
		if chmodErr := chmodUnixProviderAtWithOps(
			parentFD, filepath.Base(path), 0o600, parentInfo, expected,
			uint32(os.Geteuid()), ops,
		); chmodErr != nil {
			t.Fatal(chmodErr)
		}
		if !reflect.DeepEqual(flags, []int{unix.AT_SYMLINK_NOFOLLOW, 0}) {
			t.Fatalf("fchmodat flags = %v", flags)
		}
		secured, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if secured.Mode().Perm() != 0o600 {
			t.Fatalf("seccomp fallback mode = %04o", secured.Mode().Perm())
		}
	})

	if chmodErr := chmodUnixProviderAtWithOps(
		parentFD, "missing", 0o600, parentInfo, parentInfo,
		uint32(os.Geteuid()), unixProviderAtOps{},
	); chmodErr == nil {
		t.Fatal("missing relative chmod operations were accepted")
	}
}

func TestValidateProviderGenerationFileRejectsUnsafeFinalMetadata(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "mode changed", mutate: func(t *testing.T, path string) {
			if err := os.Chmod(path, 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "link count changed", mutate: func(t *testing.T, path string) {
			if err := os.Link(path, path+".alias"); err != nil {
				t.Skipf("hardlinks unavailable: %v", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "store.db")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			expected, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, path)
			current, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(expected, current) {
				t.Fatal("metadata fixture changed file identity")
			}
			filesystem := providerFilesystem{
				secureFile: func(string) error { return nil },
				lstat:      func(string) (os.FileInfo, error) { return current, nil },
				validateLiveInfo: func(info os.FileInfo) error {
					return validateUnixProviderLiveInfo(info, uint32(os.Geteuid()))
				},
			}
			if _, err := validateProviderGenerationFile(
				filesystem, path, expected,
			); err == nil || !errors.Is(err, errProviderUnsafeBoundary) {
				t.Fatalf("unsafe final metadata error = %v", err)
			}
		})
	}
}

func TestGenerationValidationFailureDisambiguatesOptionalTransitions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	expectedPath := filepath.Join(root, "expected-wal")
	replacementPath := filepath.Join(root, "replacement-wal")
	for _, path := range []string{expectedPath, replacementPath} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.Lstat(expectedPath)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := os.Lstat(replacementPath)
	if err != nil {
		t.Fatal(err)
	}
	canary := errors.New("owner or platform validation unavailable")
	for _, test := range []struct {
		name              string
		current           os.FileInfo
		currentErr        error
		wantTransition    bool
		wantOriginalError bool
	}{
		{name: "disappeared", currentErr: os.ErrNotExist, wantTransition: true},
		{name: "replaced", current: replacement, wantTransition: true},
		{name: "stable unavailable", current: expected, wantOriginalError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			transitioned, err := classifyGenerationFileValidationError(
				providerFilesystem{lstat: func(string) (os.FileInfo, error) {
					return test.current, test.currentErr
				}},
				"store.db-wal",
				expected,
				true,
				canary,
			)
			if transitioned != test.wantTransition {
				t.Fatalf("transitioned = %v, want %v (error %v)", transitioned, test.wantTransition, err)
			}
			if test.wantTransition && err != nil {
				t.Fatalf("transition error = %v", err)
			}
			if test.wantOriginalError && (!errors.Is(err, canary) ||
				errors.Is(err, errProviderUnsafeBoundary)) {
				t.Fatalf("stable unavailable error = %v", err)
			}
		})
	}
}

func TestGenerationValidationNeverDowngradesUnsafeOptionalMember(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	expectedPath := filepath.Join(root, "expected-wal")
	replacementPath := filepath.Join(root, "replacement-wal")
	for _, path := range []string{expectedPath, replacementPath} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.Lstat(expectedPath)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := os.Lstat(replacementPath)
	if err != nil {
		t.Fatal(err)
	}
	unsafeErr := errors.Join(errProviderUnsafeBoundary, errors.New("unsafe sidecar canary"))
	for _, test := range []struct {
		name       string
		current    os.FileInfo
		currentErr error
	}{
		{name: "then disappeared", currentErr: os.ErrNotExist},
		{name: "then replaced", current: replacement},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			transitioned, err := classifyGenerationFileValidationError(
				providerFilesystem{lstat: func(string) (os.FileInfo, error) {
					return test.current, test.currentErr
				}},
				"store.db-wal",
				expected,
				true,
				unsafeErr,
			)
			if transitioned || !errors.Is(err, errProviderUnsafeBoundary) ||
				!strings.Contains(err.Error(), "unsafe sidecar canary") {
				t.Fatalf("unsafe optional validation = transitioned:%v error:%v", transitioned, err)
			}
		})
	}
}

func TestGenerationRejectsNonHardenableOptionalBeforeTransition(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mainPath := filepath.Join(root, "store.db")
	walPath := mainPath + "-wal"
	if err := os.WriteFile(mainPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(walPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(walPath, 0o400); err != nil {
		t.Fatal(err)
	}
	for _, validationErr := range []error{os.ErrNotExist, errProviderGenerationTransition} {
		t.Run(validationErr.Error(), func(t *testing.T) {
			t.Parallel()
			optionalSecureCalls := 0
			filesystem := systemProviderFilesystem()
			filesystem.secureFile = func(path string) error {
				if path == walPath {
					optionalSecureCalls++
					return validationErr
				}
				return nil
			}
			err := validateGenerationMembersWithFilesystem(mainPath, true, filesystem)
			if err == nil || !errors.Is(err, errProviderUnsafeBoundary) ||
				errors.Is(err, errProviderGenerationTransition) {
				t.Fatalf("non-hardenable optional error = %v", err)
			}
			if optionalSecureCalls != 0 {
				t.Fatalf("optional secure calls = %d, want 0", optionalSecureCalls)
			}
		})
	}
}

func TestGenerationOwnerClassificationDisambiguatesTransition(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	expectedPath := filepath.Join(root, "expected-wal")
	replacementPath := filepath.Join(root, "replacement-wal")
	for _, path := range []string{expectedPath, replacementPath} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.Lstat(expectedPath)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := os.Lstat(replacementPath)
	if err != nil {
		t.Fatal(err)
	}
	base := func(owner generationOwnerClass, current os.FileInfo, currentErr error) (bool, error) {
		return validateGenerationMemberMetadata(
			"store.db-wal",
			expected,
			true,
			providerFilesystem{
				lstat: func(string) (os.FileInfo, error) { return current, currentErr },
				validateLiveInfo: func(info os.FileInfo) error {
					return validateUnixProviderLiveInfo(info, uint32(os.Geteuid()))
				},
				linkCount: func(string, os.FileInfo) generationLinkClass { return generationLinkSingle },
				owner:     func(string, os.FileInfo) generationOwnerClass { return owner },
			},
		)
	}
	if transitioned, err := base(generationOwnerUnavailable, nil, os.ErrNotExist); !transitioned || err != nil {
		t.Fatalf("missing optional owner check = transitioned:%v error:%v", transitioned, err)
	}
	if transitioned, err := base(generationOwnerUnavailable, replacement, nil); !transitioned || err != nil {
		t.Fatalf("replaced optional owner check = transitioned:%v error:%v", transitioned, err)
	}
	if transitioned, err := base(generationOwnerUnavailable, expected, nil); transitioned || err == nil ||
		errors.Is(err, errProviderUnsafeBoundary) {
		t.Fatalf("stable unavailable owner check = transitioned:%v error:%v", transitioned, err)
	}
	if transitioned, err := base(generationOwnerForeign, expected, nil); transitioned ||
		err == nil || !errors.Is(err, errProviderUnsafeBoundary) {
		t.Fatalf("stable foreign owner check = transitioned:%v error:%v", transitioned, err)
	}
}

func TestGenerationLinkUnavailableDisambiguatesTransition(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	expectedPath := filepath.Join(root, "expected-wal")
	replacementPath := filepath.Join(root, "replacement-wal")
	for _, path := range []string{expectedPath, replacementPath} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.Lstat(expectedPath)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := os.Lstat(replacementPath)
	if err != nil {
		t.Fatal(err)
	}
	validate := func(current os.FileInfo, currentErr error) (bool, error) {
		return validateGenerationMemberMetadata(
			"store.db-wal",
			expected,
			true,
			providerFilesystem{
				lstat: func(string) (os.FileInfo, error) { return current, currentErr },
				validateLiveInfo: func(info os.FileInfo) error {
					return validateUnixProviderLiveInfo(info, uint32(os.Geteuid()))
				},
				linkCount: func(string, os.FileInfo) generationLinkClass {
					return generationLinkUnavailable
				},
				owner: func(string, os.FileInfo) generationOwnerClass { return generationOwnerCurrent },
			},
		)
	}
	if transitioned, err := validate(nil, os.ErrNotExist); !transitioned || err != nil {
		t.Fatalf("missing optional link check = transitioned:%v error:%v", transitioned, err)
	}
	if transitioned, err := validate(replacement, nil); !transitioned || err != nil {
		t.Fatalf("replaced optional link check = transitioned:%v error:%v", transitioned, err)
	}
	if transitioned, err := validate(expected, nil); transitioned || err == nil ||
		errors.Is(err, errProviderUnsafeBoundary) {
		t.Fatalf("stable unavailable link check = transitioned:%v error:%v", transitioned, err)
	}
}

func TestGenerationUnsafeLinkBoundaryIsNotHardlinkClassification(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "store.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	transitioned, err := validateGenerationMemberMetadata(
		path,
		info,
		true,
		providerFilesystem{
			lstat: func(string) (os.FileInfo, error) { return info, nil },
			validateLiveInfo: func(info os.FileInfo) error {
				return validateUnixProviderLiveInfo(info, uint32(os.Geteuid()))
			},
			linkCount: func(string, os.FileInfo) generationLinkClass { return generationLinkUnsafe },
			owner:     func(string, os.FileInfo) generationOwnerClass { return generationOwnerCurrent },
		},
	)
	if transitioned || err == nil || !errors.Is(err, errProviderUnsafeBoundary) ||
		strings.Contains(strings.ToLower(err.Error()), "hardlink") {
		t.Fatalf("unsafe link boundary = transitioned:%v error:%v", transitioned, err)
	}
}

func TestGenerationMetadataDefersRepairableUnixMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "store.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	transitioned, err := validateGenerationMemberMetadata(
		path,
		info,
		false,
		providerFilesystem{
			validateLiveInfo: func(info os.FileInfo) error {
				return validateUnixProviderLiveInfo(info, uint32(os.Geteuid()))
			},
			linkCount: func(string, os.FileInfo) generationLinkClass { return generationLinkSingle },
			owner:     func(string, os.FileInfo) generationOwnerClass { return generationOwnerCurrent },
		},
	)
	if transitioned || err != nil {
		t.Fatalf("repairable Unix mode = transitioned:%v error:%v", transitioned, err)
	}
}

func TestSecureGenerationHardensLiveFilesWithoutOpeningThem(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Chmod(root, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "store.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := SecureGeneration(path); err != nil {
		t.Fatal(err)
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("SecureGeneration mode = %04o", got)
	}
	parentInfo, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := parentInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("SecureGeneration parent mode = %04o", got)
	}
}

func TestPrepareStoreHardensNewAndLegacyMain(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	created := filepath.Join(root, "created.db")
	if err := PrepareStore(created); err != nil {
		t.Fatal(err)
	}
	createdInfo, err := os.Lstat(created)
	if err != nil {
		t.Fatal(err)
	}
	if got := createdInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("created mode = %04o", got)
	}

	existing := filepath.Join(root, "existing.db")
	if writeErr := os.WriteFile(existing, nil, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if chmodErr := os.Chmod(existing, 0o640); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	if prepareErr := PrepareStore(existing); prepareErr != nil {
		t.Fatal(prepareErr)
	}
	existingInfo, statErr := os.Lstat(existing)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := existingInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("existing mode = %04o", got)
	}
}

func TestSecureGenerationHardensLiveRawSQLiteGeneration(t *testing.T) {
	if path := os.Getenv(providerLegacyRawSQLiteHelperPath); path != "" {
		previousUmask := unix.Umask(0o002)
		defer unix.Umask(previousUmask)
		database, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		database.SetMaxOpenConns(1)
		database.SetMaxIdleConns(1)
		connection, err := database.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		var journal string
		if err := connection.QueryRowContext(
			t.Context(), "PRAGMA journal_mode = WAL",
		).Scan(&journal); err != nil || !strings.EqualFold(journal, "wal") {
			t.Fatalf("journal = %q, %v", journal, err)
		}
		if _, err := connection.ExecContext(
			t.Context(), "CREATE TABLE entries (value TEXT NOT NULL)",
		); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.ExecContext(
			t.Context(), "INSERT INTO entries(value) VALUES ('before')",
		); err != nil {
			t.Fatal(err)
		}
		members := []string{path, path + "-wal", path + "-shm"}
		for _, member := range members {
			info, err := os.Lstat(member)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() == 0o600 {
				t.Fatalf("raw SQLite member %s was already private", filepath.Base(member))
			}
			if err := validateUnixProviderHardenableMode(info.Mode()); err != nil {
				t.Fatalf("raw SQLite member %s mode %04o: %v", member, info.Mode().Perm(), err)
			}
		}
		if err := SecureGeneration(path); err != nil {
			t.Fatal(err)
		}
		for _, member := range members {
			info, err := os.Lstat(member)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("secured SQLite member %s mode = %04o", member, info.Mode().Perm())
			}
		}
		if _, err := connection.ExecContext(
			t.Context(), "INSERT INTO entries(value) VALUES ('after')",
		); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := connection.QueryRowContext(
			t.Context(), "SELECT COUNT(*) FROM entries",
		).Scan(&count); err != nil || count != 2 {
			t.Fatalf("row count = %d, %v", count, err)
		}
		return
	}

	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "legacy.db")
	command := exec.Command(os.Args[0], "-test.run=^TestSecureGenerationHardensLiveRawSQLiteGeneration$")
	command.Env = append(os.Environ(), providerLegacyRawSQLiteHelperPath+"="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("raw SQLite helper = %v\n%s", err, output)
	}
}

func TestSecureGenerationPreservesPOSIXRecordLock(t *testing.T) {
	if helperPath := os.Getenv(providerPOSIXLockHelperPath); helperPath != "" {
		file, err := os.OpenFile(helperPath, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 1}
		err = unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock)
		if err == nil {
			lock.Type = unix.F_UNLCK
			_ = unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock)
			t.Fatal("child acquired parent's POSIX record lock")
		}
		if !errors.Is(err, unix.EACCES) && !errors.Is(err, unix.EAGAIN) {
			t.Fatalf("child lock error = %v", err)
		}
		return
	}

	path := filepath.Join(t.TempDir(), "store.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 1}
	if lockErr := unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock); lockErr != nil {
		t.Skipf("POSIX record locks unavailable: %v", lockErr)
	}
	defer func() {
		lock.Type = unix.F_UNLCK
		_ = unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock)
	}()

	assertProviderPOSIXLockHeldByParent(t, path)
	if secureErr := SecureGeneration(path); secureErr != nil {
		t.Fatal(secureErr)
	}
	assertProviderPOSIXLockHeldByParent(t, path)
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secured mode = %v", info.Mode().Perm())
	}
}

func TestSecureGenerationPreservesSHMPOSIXRecordLock(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "store.db")
	for _, path := range []string{mainPath, mainPath + "-wal", mainPath + "-shm"} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	shmPath := mainPath + "-shm"
	file, err := os.OpenFile(shmPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 1}
	if lockErr := unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock); lockErr != nil {
		t.Skipf("POSIX record locks unavailable: %v", lockErr)
	}
	defer func() {
		lock.Type = unix.F_UNLCK
		_ = unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock)
	}()

	assertProviderPOSIXLockHeldByParent(t, shmPath)
	if secureErr := SecureGeneration(mainPath); secureErr != nil {
		t.Fatal(secureErr)
	}
	assertProviderPOSIXLockHeldByParent(t, shmPath)
	for _, path := range []string{mainPath, mainPath + "-wal", mainPath + "-shm"} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("secured %s mode = %v", path, info.Mode().Perm())
		}
	}
}

func TestPrepareStoreExistingPreservesPOSIXRecordLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 1}
	if lockErr := unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock); lockErr != nil {
		t.Skipf("POSIX record locks unavailable: %v", lockErr)
	}
	defer func() {
		lock.Type = unix.F_UNLCK
		_ = unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock)
	}()

	assertProviderPOSIXLockHeldByParent(t, path)
	if prepareErr := PrepareStore(path); prepareErr != nil {
		t.Fatal(prepareErr)
	}
	assertProviderPOSIXLockHeldByParent(t, path)
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secured mode = %v", info.Mode().Perm())
	}
}

func assertProviderPOSIXLockHeldByParent(t *testing.T, path string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestSecureGenerationPreservesPOSIXRecordLock$")
	command.Env = append(os.Environ(), providerPOSIXLockHelperPath+"="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("record-lock helper = %v\n%s", err, output)
	}
}

func TestGenerationLinkTransitionsAreBoundedAndDistinct(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "logical-store.db")
	identityPaths := []string{
		filepath.Join(root, "main-identity"),
		filepath.Join(root, "wal-identity"),
		filepath.Join(root, "replacement-identity"),
	}
	identities := make([]os.FileInfo, len(identityPaths))
	for index, identityPath := range identityPaths {
		if err := os.WriteFile(identityPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(identityPath)
		if err != nil {
			t.Fatal(err)
		}
		identities[index] = info
	}
	mainInfo, walInfo, replacementInfo := identities[0], identities[1], identities[2]

	t.Run("main zero is fatal", func(t *testing.T) {
		filesystem := stableUnixGenerationFilesystem(mainPath, mainInfo, nil)
		filesystem.linkCount = func(candidate string, _ os.FileInfo) generationLinkClass {
			if candidate == mainPath {
				return generationLinkZero
			}
			return generationLinkSingle
		}
		err := validateGenerationMembersWithFilesystem(mainPath, true, filesystem)
		if err == nil || !errors.Is(err, errProviderUnsafeBoundary) ||
			strings.Contains(strings.ToLower(err.Error()), "hardlink") {
			t.Fatalf("main zero-link error = %v", err)
		}
	})

	t.Run("optional zero retries then fails closed", func(t *testing.T) {
		filesystem := stableUnixGenerationFilesystem(mainPath, mainInfo, walInfo)
		zeroCalls := 0
		filesystem.linkCount = func(candidate string, _ os.FileInfo) generationLinkClass {
			if candidate == mainPath+"-wal" {
				zeroCalls++
				return generationLinkZero
			}
			return generationLinkSingle
		}
		err := validateGenerationMembersWithFilesystem(mainPath, true, filesystem)
		if err == nil || errors.Is(err, errProviderUnsafeBoundary) ||
			!errors.Is(err, errProviderGenerationTransition) ||
			!strings.Contains(err.Error(), "did not stabilize") ||
			strings.Contains(strings.ToLower(err.Error()), "hardlink") {
			t.Fatalf("optional zero-link error = %v", err)
		}
		if zeroCalls != generationValidationAttempts {
			t.Fatalf("zero-link attempts = %d, want %d", zeroCalls, generationValidationAttempts)
		}
	})

	t.Run("concurrent main and WAL creation restarts missing scan", func(t *testing.T) {
		mainCalls := 0
		filesystem := stableUnixGenerationFilesystem(mainPath, mainInfo, walInfo)
		filesystem.lstat = func(candidate string) (os.FileInfo, error) {
			switch candidate {
			case mainPath:
				mainCalls++
				if mainCalls == 1 {
					return nil, os.ErrNotExist
				}
				return mainInfo, nil
			case mainPath + "-wal":
				return walInfo, nil
			default:
				return nil, os.ErrNotExist
			}
		}
		if err := validateGenerationMembersWithFilesystem(mainPath, false, filesystem); err != nil {
			t.Fatalf("concurrent main creation = %v", err)
		}
		if mainCalls < 2 {
			t.Fatalf("main inspections = %d, want a whole-scan restart", mainCalls)
		}
	})

	t.Run("mid-scan WAL creation retries before coherence", func(t *testing.T) {
		shmPath := filepath.Join(root, "shm-identity")
		if err := os.WriteFile(shmPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		shmInfo, err := os.Lstat(shmPath)
		if err != nil {
			t.Fatal(err)
		}
		walCalls := 0
		filesystem := stableUnixGenerationFilesystem(mainPath, mainInfo, walInfo)
		filesystem.lstat = func(candidate string) (os.FileInfo, error) {
			switch candidate {
			case mainPath:
				return mainInfo, nil
			case mainPath + "-wal":
				walCalls++
				if walCalls == 1 {
					return nil, os.ErrNotExist
				}
				return walInfo, nil
			case mainPath + "-shm":
				return shmInfo, nil
			default:
				return nil, os.ErrNotExist
			}
		}
		if err := validateGenerationMembersWithFilesystem(mainPath, true, filesystem); err != nil {
			t.Fatalf("mid-scan WAL creation = %v", err)
		}
		if walCalls < 2 {
			t.Fatalf("WAL inspections = %d, want a whole-scan restart", walCalls)
		}
	})

	t.Run("one replacement restarts whole scan", func(t *testing.T) {
		replacement := replacementInfo
		filesystem := stableUnixGenerationFilesystem(mainPath, mainInfo, nil)
		walCalls := 0
		mainSecurityCalls := 0
		filesystem.lstat = func(candidate string) (os.FileInfo, error) {
			switch candidate {
			case mainPath:
				return mainInfo, nil
			case mainPath + "-wal":
				walCalls++
				if walCalls == 1 {
					return walInfo, nil
				}
				return replacement, nil
			default:
				return nil, os.ErrNotExist
			}
		}
		filesystem.secureFile = func(candidate string) error {
			if candidate == mainPath {
				mainSecurityCalls++
			}
			return nil
		}
		if err := validateGenerationMembersWithFilesystem(mainPath, true, filesystem); err != nil {
			t.Fatal(err)
		}
		if mainSecurityCalls <= 2 {
			t.Fatalf("main validations = %d, want whole-scan restart", mainSecurityCalls)
		}
	})

	t.Run("continuous replacement fails closed", func(t *testing.T) {
		replacement := replacementInfo
		filesystem := stableUnixGenerationFilesystem(mainPath, mainInfo, nil)
		walCalls := 0
		filesystem.lstat = func(candidate string) (os.FileInfo, error) {
			switch candidate {
			case mainPath:
				return mainInfo, nil
			case mainPath + "-wal":
				walCalls++
				if walCalls%2 == 1 {
					return walInfo, nil
				}
				return replacement, nil
			default:
				return nil, os.ErrNotExist
			}
		}
		err := validateGenerationMembersWithFilesystem(mainPath, true, filesystem)
		if err == nil || !strings.Contains(err.Error(), "did not stabilize") ||
			strings.Contains(strings.ToLower(err.Error()), "hardlink") {
			t.Fatalf("continuous replacement error = %v", err)
		}
	})
}

func stableUnixGenerationFilesystem(
	mainPath string,
	mainInfo os.FileInfo,
	walInfo os.FileInfo,
) providerFilesystem {
	return providerFilesystem{
		lstat: func(candidate string) (os.FileInfo, error) {
			switch candidate {
			case mainPath:
				return mainInfo, nil
			case mainPath + "-wal":
				if walInfo != nil {
					return walInfo, nil
				}
			}
			return nil, os.ErrNotExist
		},
		secureFile: func(string) error { return nil },
		validateLiveInfo: func(info os.FileInfo) error {
			return validateUnixProviderLiveInfo(info, uint32(os.Geteuid()))
		},
		linkCount: func(string, os.FileInfo) generationLinkClass {
			return generationLinkSingle
		},
		owner: func(string, os.FileInfo) generationOwnerClass { return generationOwnerCurrent },
	}
}
