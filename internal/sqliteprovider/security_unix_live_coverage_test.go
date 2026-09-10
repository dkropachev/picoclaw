//go:build linux

package sqliteprovider

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestCoverageSecureProviderFileMissing(t *testing.T) {
	t.Parallel()

	err := secureProviderFile(filepath.Join(t.TempDir(), "missing.db"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("secure missing file error = %v, want not-exist", err)
	}
}

func TestCoverageSecureUnixProviderHandleRejectsDirectoryAsFile(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	info := lstatUnixCoverageFile(t, path)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if secureErr := secureUnixProviderHandle(
		path, false, info, file, uint32(os.Geteuid()),
	); secureErr == nil || !errors.Is(secureErr, errProviderUnsafeBoundary) {
		t.Fatalf("directory-as-file error = %v", secureErr)
	}
}

func TestCoverageHardenUnixProviderLiveFileModePreChmodFailures(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	referencePath := filepath.Join(root, "reference.db")
	writeUnixCoverageFile(t, referencePath, 0o600)
	reference := lstatUnixCoverageFile(t, referencePath)

	t.Run("missing parent", func(t *testing.T) {
		t.Parallel()

		err := hardenUnixProviderLiveFileMode(
			filepath.Join(root, "missing", "store.db"), reference, 0o600,
		)
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("error = %v, want not-exist", err)
		}
	})

	t.Run("parent is not directory", func(t *testing.T) {
		t.Parallel()

		parent := filepath.Join(root, "regular-parent")
		writeUnixCoverageFile(t, parent, 0o600)
		if err := hardenUnixProviderLiveFileMode(
			filepath.Join(parent, "store.db"), reference, 0o600,
		); err == nil {
			t.Fatal("regular file was accepted as live-file parent")
		}
	})

	t.Run("parent cannot be secured", func(t *testing.T) {
		t.Parallel()

		parent := filepath.Join("/proc", "self", "fd")
		if _, err := os.Lstat(parent); err != nil {
			t.Skipf("proc descriptor directory unavailable: %v", err)
		}
		err := hardenUnixProviderLiveFileMode(
			filepath.Join(parent, "missing"), reference, 0o600,
		)
		if err == nil {
			t.Fatal("read-only proc parent was secured")
		}
	})

	t.Run("missing child", func(t *testing.T) {
		t.Parallel()

		directory := filepath.Join(root, "missing-child-parent")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		err := hardenUnixProviderLiveFileMode(
			filepath.Join(directory, "missing.db"), reference, 0o600,
		)
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("error = %v, want not-exist", err)
		}
	})

	t.Run("child identity mismatch", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(root, "mismatch.db")
		writeUnixCoverageFile(t, path, 0o640)
		err := hardenUnixProviderLiveFileMode(path, reference, 0o600)
		if !errors.Is(err, errProviderGenerationTransition) {
			t.Fatalf("error = %v, want generation transition", err)
		}
	})

	t.Run("symlink child", func(t *testing.T) {
		t.Parallel()

		target := filepath.Join(root, "symlink-target.db")
		link := filepath.Join(root, "symlink.db")
		writeUnixCoverageFile(t, target, 0o640)
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("create symlink: %v", err)
		}
		err := hardenUnixProviderLiveFileMode(link, lstatUnixCoverageFile(t, link), 0o600)
		if !errors.Is(err, errProviderUnsafeBoundary) ||
			!strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("error = %v, want unsafe non-regular file", err)
		}
	})

	t.Run("hardlink child", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(root, "hardlink.db")
		writeUnixCoverageFile(t, path, 0o640)
		if err := os.Link(path, path+".alias"); err != nil {
			t.Skipf("create hardlink: %v", err)
		}
		err := hardenUnixProviderLiveFileMode(path, lstatUnixCoverageFile(t, path), 0o600)
		if !errors.Is(err, errProviderUnsafeBoundary) ||
			!strings.Contains(err.Error(), "hardlink alias") {
			t.Fatalf("error = %v, want unsafe hardlink", err)
		}
	})

	t.Run("mode cannot be narrowed", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(root, "read-only.db")
		writeUnixCoverageFile(t, path, 0o400)
		err := hardenUnixProviderLiveFileMode(path, lstatUnixCoverageFile(t, path), 0o600)
		if !errors.Is(err, errProviderUnsafeBoundary) ||
			!strings.Contains(err.Error(), "cannot be safely narrowed") {
			t.Fatalf("error = %v, want unsafe non-hardenable mode", err)
		}
	})

	t.Run("narrows mode", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(root, "hardenable.db")
		writeUnixCoverageFile(t, path, 0o640)
		if err := hardenUnixProviderLiveFileMode(
			path, lstatUnixCoverageFile(t, path), 0o600,
		); err != nil {
			t.Fatal(err)
		}
		if got := lstatUnixCoverageFile(t, path).Mode().Perm(); got != 0o600 {
			t.Fatalf("secured mode = %04o, want 0600", got)
		}
	})

	t.Run("relative chmod fails", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(root, "chmod-error.db")
		writeUnixCoverageFile(t, path, 0o640)
		err, filterErr := runUnixCoverageWithFchmodatResult(
			path, lstatUnixCoverageFile(t, path),
			unix.SECCOMP_RET_ERRNO|uint32(unix.EIO),
		)
		if filterErr != nil {
			t.Skipf("install per-thread seccomp filter: %v", filterErr)
		}
		if !errors.Is(err, unix.EIO) {
			t.Fatalf("error = %v, want EIO", err)
		}
	})

	t.Run("relative chmod reports success without narrowing", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(root, "chmod-ineffective.db")
		writeUnixCoverageFile(t, path, 0o640)
		err, filterErr := runUnixCoverageWithFchmodatResult(
			path, lstatUnixCoverageFile(t, path), unix.SECCOMP_RET_ERRNO,
		)
		if filterErr != nil {
			t.Skipf("install per-thread seccomp filter: %v", filterErr)
		}
		if !errors.Is(err, errProviderUnsafeBoundary) ||
			!errors.Is(err, errProviderFileModeNeedsHardening) {
			t.Fatalf("error = %v, want unsafe mode-hardening failure", err)
		}
	})
}

func TestCoverageChmodUnixProviderAtWithOpsFailures(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(root, "store.db")
	writeUnixCoverageFile(t, childPath, 0o640)
	parentExpected := lstatUnixCoverageFile(t, root)
	childExpected := lstatUnixCoverageFile(t, childPath)
	parentStat := lstatUnixCoverageStat(t, root)
	childStat := lstatUnixCoverageStat(t, childPath)
	currentUID := uint32(os.Geteuid())
	canary := errors.New("coverage canary")

	tests := []struct {
		name              string
		firstChmodError   error
		parentStatError   error
		mutateParent      func(*unix.Stat_t)
		childStatError    error
		mutateChild       func(*unix.Stat_t)
		fallbackChmodErr  error
		wantError         error
		wantUnsafe        bool
		wantFallbackCalls int
	}{
		{
			name: "non-fallback chmod error", firstChmodError: canary,
			wantError: canary,
		},
		{name: "no-follow chmod succeeds"},
		{
			name: "parent stat error", firstChmodError: unix.ENOTSUP,
			parentStatError: canary, wantError: canary,
		},
		{
			name: "unsafe parent", firstChmodError: unix.ENOTSUP,
			mutateParent: func(stat *unix.Stat_t) { stat.Mode = unix.S_IFREG | 0o600 },
			wantUnsafe:   true,
		},
		{
			name: "parent identity mismatch", firstChmodError: unix.ENOTSUP,
			mutateParent: func(stat *unix.Stat_t) { stat.Ino++ },
			wantError:    errProviderGenerationTransition,
		},
		{
			name: "child stat error", firstChmodError: unix.ENOTSUP,
			childStatError: canary, wantError: canary,
		},
		{
			name: "child identity mismatch", firstChmodError: unix.ENOTSUP,
			mutateChild: func(stat *unix.Stat_t) { stat.Ino++ },
			wantError:   errProviderGenerationTransition,
		},
		{
			name: "unsafe child", firstChmodError: unix.ENOTSUP,
			mutateChild: func(stat *unix.Stat_t) { stat.Nlink = 2 },
			wantUnsafe:  true,
		},
		{
			name: "child mode cannot be narrowed", firstChmodError: unix.ENOTSUP,
			mutateChild: func(stat *unix.Stat_t) { stat.Mode = unix.S_IFREG | 0o400 },
			wantUnsafe:  true,
		},
		{
			name: "fallback chmod error", firstChmodError: unix.ENOTSUP,
			fallbackChmodErr: canary, wantError: canary, wantFallbackCalls: 1,
		},
		{
			name: "fallback succeeds", firstChmodError: unix.ENOTSUP,
			wantFallbackCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			gotParent := parentStat
			if test.mutateParent != nil {
				test.mutateParent(&gotParent)
			}
			gotChild := childStat
			if test.mutateChild != nil {
				test.mutateChild(&gotChild)
			}
			fallbackCalls := 0
			err := chmodUnixProviderAtWithOps(
				42, "store.db", 0o600, parentExpected, childExpected, currentUID,
				unixProviderAtOps{
					fchmodat: func(_ int, _ string, _ uint32, flags int) error {
						if flags == unix.AT_SYMLINK_NOFOLLOW {
							return test.firstChmodError
						}
						fallbackCalls++
						return test.fallbackChmodErr
					},
					fstat: func(_ int, stat *unix.Stat_t) error {
						*stat = gotParent
						return test.parentStatError
					},
					fstatat: func(_ int, _ string, stat *unix.Stat_t, _ int) error {
						*stat = gotChild
						return test.childStatError
					},
				},
			)
			if test.wantUnsafe {
				if !errors.Is(err, errProviderUnsafeBoundary) {
					t.Fatalf("error = %v, want unsafe boundary", err)
				}
			} else if !errors.Is(err, test.wantError) {
				t.Fatalf("error = %v, want %v", err, test.wantError)
			}
			if fallbackCalls != test.wantFallbackCalls {
				t.Fatalf(
					"fallback chmod calls = %d, want %d",
					fallbackCalls,
					test.wantFallbackCalls,
				)
			}
		})
	}

	if err := chmodUnixProviderAtWithOps(
		42, "store.db", 0o600, parentExpected, childExpected, currentUID,
		unixProviderAtOps{},
	); err == nil {
		t.Fatal("nil relative-chmod operations were accepted")
	}
}

func TestCoverageValidateUnixProviderLiveStat(t *testing.T) {
	t.Parallel()

	currentUID := uint32(os.Geteuid())
	valid := unix.Stat_t{
		Mode:  unix.S_IFREG | 0o600,
		Uid:   currentUID,
		Nlink: 1,
	}
	tests := []struct {
		name       string
		stat       *unix.Stat_t
		exactMode  bool
		wantError  error
		wantUnsafe bool
		wantText   string
	}{
		{name: "nil", wantUnsafe: true, wantText: "not a regular file"},
		{
			name: "directory", stat: &unix.Stat_t{Mode: unix.S_IFDIR | 0o700},
			wantUnsafe: true, wantText: "not a regular file",
		},
		{
			name:       "foreign owner",
			stat:       &unix.Stat_t{Mode: unix.S_IFREG | 0o600, Uid: currentUID + 1, Nlink: 1},
			wantUnsafe: true, wantText: "another user",
		},
		{
			name:      "zero links",
			stat:      &unix.Stat_t{Mode: unix.S_IFREG | 0o600, Uid: currentUID},
			wantError: errProviderGenerationTransition,
		},
		{
			name:       "hardlink",
			stat:       &unix.Stat_t{Mode: unix.S_IFREG | 0o600, Uid: currentUID, Nlink: 2},
			wantUnsafe: true, wantText: "hardlink alias",
		},
		{name: "valid non-exact", stat: cloneUnixCoverageStat(valid)},
		{name: "valid exact", stat: cloneUnixCoverageStat(valid), exactMode: true},
		{
			name:      "non-hardenable exact mode",
			stat:      &unix.Stat_t{Mode: unix.S_IFREG | 0o400, Uid: currentUID, Nlink: 1},
			exactMode: true, wantUnsafe: true, wantText: "cannot be safely narrowed",
		},
		{
			name: "hardenable non-exact mode",
			stat: &unix.Stat_t{Mode: unix.S_IFREG | 0o640, Uid: currentUID, Nlink: 1},
		},
		{
			name:      "hardenable exact mode",
			stat:      &unix.Stat_t{Mode: unix.S_IFREG | 0o640, Uid: currentUID, Nlink: 1},
			exactMode: true, wantUnsafe: true, wantText: "mode is not 0600",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateUnixProviderLiveStat(test.stat, currentUID, test.exactMode)
			if test.wantUnsafe {
				if !errors.Is(err, errProviderUnsafeBoundary) {
					t.Fatalf("error = %v, want unsafe boundary", err)
				}
			} else if !errors.Is(err, test.wantError) {
				t.Fatalf("error = %v, want %v", err, test.wantError)
			}
			if test.wantText != "" && !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("error = %v, want text %q", err, test.wantText)
			}
		})
	}
}

func TestCoverageValidateUnixProviderParentStat(t *testing.T) {
	t.Parallel()

	currentUID := uint32(os.Geteuid())
	for _, test := range []struct {
		name string
		stat *unix.Stat_t
		want bool
	}{
		{name: "nil", want: false},
		{name: "regular file", stat: &unix.Stat_t{Mode: unix.S_IFREG | 0o600, Uid: currentUID}},
		{name: "foreign owner", stat: &unix.Stat_t{Mode: unix.S_IFDIR | 0o700, Uid: currentUID + 1}},
		{name: "wrong mode", stat: &unix.Stat_t{Mode: unix.S_IFDIR | 0o755, Uid: currentUID}},
		{name: "valid", stat: &unix.Stat_t{Mode: unix.S_IFDIR | 0o700, Uid: currentUID}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateUnixProviderParentStat(test.stat, currentUID)
			if test.want && err != nil {
				t.Fatal(err)
			}
			if !test.want && !errors.Is(err, errProviderUnsafeBoundary) {
				t.Fatalf("error = %v, want unsafe boundary", err)
			}
		})
	}
}

func TestCoverageValidateSecuredUnixProviderLiveFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	expectedPath := filepath.Join(root, "expected.db")
	replacementPath := filepath.Join(root, "replacement.db")
	writeUnixCoverageFile(t, expectedPath, 0o600)
	writeUnixCoverageFile(t, replacementPath, 0o600)
	expected := lstatUnixCoverageFile(t, expectedPath)
	expectedStat := lstatUnixCoverageStat(t, expectedPath)
	replacementStat := lstatUnixCoverageStat(t, replacementPath)
	canary := errors.New("secured live-file coverage canary")
	for _, test := range []struct {
		name string
		stat *unix.Stat_t
		err  error
		want error
		any  bool
	}{
		{name: "stat unavailable", any: true},
		{name: "stat error", err: canary, want: canary},
		{name: "identity changed", stat: &replacementStat, want: errProviderGenerationTransition},
		{name: "valid", stat: &expectedStat},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stat func(*unix.Stat_t) error
			if test.stat != nil || test.err != nil {
				stat = func(result *unix.Stat_t) error {
					if test.stat != nil {
						*result = *test.stat
					}
					return test.err
				}
			}
			err := validateSecuredUnixProviderLiveFileWithStat(
				expected, uint32(os.Geteuid()), stat,
			)
			if test.any && err == nil {
				t.Fatal("missing secured live-file stat succeeded")
			}
			if !test.any && !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func writeUnixCoverageFile(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, nil, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func lstatUnixCoverageFile(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func lstatUnixCoverageStat(t *testing.T, path string) unix.Stat_t {
	t.Helper()
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		t.Fatal(err)
	}
	return stat
}

func cloneUnixCoverageStat(stat unix.Stat_t) *unix.Stat_t {
	cloned := stat
	return &cloned
}

type unixCoverageHardenResult struct {
	err       error
	filterErr error
}

func runUnixCoverageWithFchmodatResult(
	path string,
	expected os.FileInfo,
	action uint32,
) (error, error) {
	result := make(chan unixCoverageHardenResult)
	go func() {
		// A seccomp filter without TSYNC affects only this OS thread. Leaving it
		// locked makes the runtime retire the thread when this goroutine exits,
		// so no subsequent test inherits the syscall behavior.
		runtime.LockOSThread()
		if err := installUnixCoverageFchmodatFilter(action); err != nil {
			result <- unixCoverageHardenResult{filterErr: err}
			return
		}
		result <- unixCoverageHardenResult{
			err: hardenUnixProviderLiveFileMode(path, expected, 0o600),
		}
	}()
	got := <-result
	return got.err, got.filterErr
}

func installUnixCoverageFchmodatFilter(action uint32) error {
	filters := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		{
			Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K,
			Jt:   1,
			K:    uint32(unix.SYS_FCHMODAT2),
		},
		{
			Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K,
			Jf:   1,
			K:    uint32(unix.SYS_FCHMODAT),
		},
		{Code: unix.BPF_RET | unix.BPF_K, K: action},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	}
	program := &unix.SockFprog{
		Len:    uint16(len(filters)),
		Filter: &filters[0],
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	_, _, syscallErr := unix.RawSyscall6(
		unix.SYS_PRCTL,
		unix.PR_SET_SECCOMP,
		unix.SECCOMP_MODE_FILTER,
		uintptr(unsafe.Pointer(program)),
		0,
		0,
		0,
	)
	runtime.KeepAlive(program)
	runtime.KeepAlive(filters)
	if syscallErr != 0 {
		return syscallErr
	}
	return nil
}
