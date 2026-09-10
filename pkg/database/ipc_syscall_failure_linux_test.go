//go:build linux && amd64

package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

type linuxSyscallFaultResult struct {
	callErr  error
	setupErr error
}

// callWithDeniedLinuxSyscalls exercises real OS failure handling without a
// mutable production seam. The locked goroutine exits with its filtered
// thread, so the process's other tests retain their normal syscall surface.
func callWithDeniedLinuxSyscalls(
	t *testing.T,
	syscalls []uint32,
	call func() error,
) error {
	t.Helper()
	result := make(chan linuxSyscallFaultResult, 1)
	go func() {
		runtime.LockOSThread()
		if err := installDeniedLinuxSyscalls(syscalls); err != nil {
			result <- linuxSyscallFaultResult{setupErr: err}
			return
		}
		result <- linuxSyscallFaultResult{callErr: call()}
	}()
	got := <-result
	if got.setupErr != nil {
		t.Fatalf("install syscall fault filter: %v", got.setupErr)
	}
	return got.callErr
}

func callWithLateDeniedLinuxSyscalls(
	t *testing.T,
	syscalls []uint32,
	call func(func() error) error,
) error {
	t.Helper()
	result := make(chan linuxSyscallFaultResult, 1)
	go func() {
		runtime.LockOSThread()
		result <- linuxSyscallFaultResult{callErr: call(func() error {
			return installDeniedLinuxSyscalls(syscalls)
		})}
	}()
	got := <-result
	return got.callErr
}

func installDeniedLinuxSyscalls(syscalls []uint32) error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	filters := []unix.SockFilter{{
		Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS,
		K:    0, // seccomp_data.nr
	}}
	for _, number := range syscalls {
		filters = append(filters,
			unix.SockFilter{
				Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K,
				Jf:   1,
				K:    number,
			},
			unix.SockFilter{
				Code: unix.BPF_RET | unix.BPF_K,
				K:    unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM),
			},
		)
	}
	filters = append(filters, unix.SockFilter{
		Code: unix.BPF_RET | unix.BPF_K,
		K:    unix.SECCOMP_RET_ALLOW,
	})
	program := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	return unix.Prctl(
		unix.PR_SET_SECCOMP,
		unix.SECCOMP_MODE_FILTER,
		uintptr(unsafe.Pointer(&program)),
		0,
		0,
	)
}

func TestLinuxFileSecurityHelpersCleanUpAfterKernelFailures(t *testing.T) {
	t.Run("inspect lock", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "storage.lock")
		err := callWithDeniedLinuxSyscalls(t, []uint32{unix.SYS_FSTAT}, func() error {
			file, acquireErr := acquirePlatformFileLock(path, false)
			if file != nil {
				_ = file.Close()
				return errors.New("lock acquisition returned a file after failed inspection")
			}
			return acquireErr
		})
		if err == nil || !strings.Contains(err.Error(), "inspect database storage lock") {
			t.Fatalf("lock inspection failure = %v", err)
		}
	})

	t.Run("revalidate lock path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "storage.lock")
		err := callWithDeniedLinuxSyscalls(t, []uint32{unix.SYS_NEWFSTATAT}, func() error {
			file, acquireErr := acquirePlatformFileLock(path, false)
			if file != nil {
				_ = file.Close()
				return errors.New("lock acquisition skipped pathname revalidation")
			}
			return acquireErr
		})
		if CodeOf(err) != CodeIntegrity ||
			!strings.Contains(err.Error(), "database storage lock changed while opening") {
			t.Fatalf("lock path revalidation failure = %v", err)
		}
	})

	t.Run("secure temporary file", func(t *testing.T) {
		directory := t.TempDir()
		err := callWithDeniedLinuxSyscalls(t, []uint32{unix.SYS_FCHMOD}, func() error {
			file, createErr := createOwnerOnlyTempFile(directory, "private-", 0o600)
			if file != nil {
				_ = file.Close()
				return errors.New("temporary-file creation returned an unsecured file")
			}
			return createErr
		})
		if err == nil {
			t.Fatal("temporary-file chmod failure was ignored")
		}
		entries, readErr := os.ReadDir(directory)
		if readErr != nil || len(entries) != 0 {
			t.Fatalf("failed secure temporary file was not removed: %v, %v", entries, readErr)
		}
	})

	t.Run("secure new home", func(t *testing.T) {
		parent := t.TempDir()
		home := filepath.Join(parent, "home")
		err := callWithDeniedLinuxSyscalls(t, []uint32{
			unix.SYS_CHMOD, unix.SYS_FCHMODAT, unix.SYS_FCHMODAT2,
		}, func() error {
			_, prepareErr := PrepareHome(home)
			return prepareErr
		})
		if err == nil || !strings.Contains(err.Error(), "secure PicoClaw home") {
			t.Fatalf("home chmod failure = %v", err)
		}
	})
}

func TestLinuxManifestOperationsPropagateKernelFailures(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		home, _, manifest := writeLinuxFaultManifest(t)
		err := callWithDeniedLinuxSyscalls(
			t,
			[]uint32{unix.SYS_READ, unix.SYS_PREAD64},
			func() error {
				_, readErr := ReadManifest(home)
				return readErr
			},
		)
		if err == nil || !strings.Contains(err.Error(), "read database broker manifest") {
			t.Fatalf("manifest read failure = %v (manifest=%#v)", err, manifest)
		}
	})

	t.Run("remove", func(t *testing.T) {
		home, _, manifest := writeLinuxFaultManifest(t)
		err := callWithDeniedLinuxSyscalls(
			t,
			[]uint32{unix.SYS_UNLINK, unix.SYS_UNLINKAT},
			func() error { return removeManifestForEpoch(home, manifest.Epoch) },
		)
		if err == nil || !strings.Contains(err.Error(), "remove database broker manifest") {
			t.Fatalf("manifest removal failure = %v", err)
		}
	})

	for _, test := range []struct {
		name     string
		syscalls []uint32
		want     string
	}{
		{name: "sync", syscalls: []uint32{unix.SYS_FSYNC}, want: "sync database broker manifest"},
		{name: "close", syscalls: []uint32{unix.SYS_CLOSE}, want: "close database broker manifest"},
		{
			name: "publish",
			syscalls: []uint32{
				unix.SYS_RENAME, unix.SYS_RENAMEAT, unix.SYS_RENAMEAT2,
			},
			want: "publish database broker manifest",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			stateDir, err := prepareStateDirectory(home)
			if err != nil {
				t.Fatal(err)
			}
			manifest := linuxFaultManifest(stateDir)
			err = callWithDeniedLinuxSyscalls(t, test.syscalls, func() error {
				return writeManifest(stateDir, manifest)
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("manifest %s failure = %v", test.name, err)
			}
		})
	}
}

func TestLinuxManifestDirectorySyncFailureRollsBackServerStartup(t *testing.T) {
	home := t.TempDir()
	var server *Server
	err := callWithLateDeniedLinuxSyscalls(
		t,
		[]uint32{unix.SYS_FSYNC},
		func(deny func() error) error {
			var startErr error
			server, startErr = StartServer(context.Background(), ServerOptions{
				Home:         home,
				StartupGuard: deny,
			})
			return startErr
		},
	)
	if server != nil || err == nil ||
		!strings.Contains(err.Error(), "sync database broker manifest directory") ||
		!strings.Contains(err.Error(), "sync rolled-back database broker manifest directory") {
		t.Fatalf("server after manifest directory-sync failure = %#v, %v", server, err)
	}
	if _, manifestErr := ReadManifest(home); CodeOf(manifestErr) != CodeUnavailable {
		t.Fatalf("directory-sync failure left discovery published: %v", manifestErr)
	}
	fence, fenceErr := AcquireMigrationFence(home)
	if fenceErr != nil {
		t.Fatalf("directory-sync failure retained online fence: %v", fenceErr)
	}
	if closeErr := fence.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	replacement, replacementErr := StartServer(context.Background(), ServerOptions{Home: home})
	if replacementErr != nil {
		t.Fatalf("directory-sync failure retained startup ownership: %v", replacementErr)
	}
	closeServer(t, replacement)
}

func TestUnixMissingHomeBelowIntermediateSymlinkIsRejected(t *testing.T) {
	root := t.TempDir()
	realParent := filepath.Join(root, "real")
	existingChild := filepath.Join(realParent, "existing")
	if err := os.MkdirAll(existingChild, 0o700); err != nil {
		t.Fatal(err)
	}
	aliasParent := filepath.Join(root, "alias")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	requested := filepath.Join(aliasParent, "existing", "new-home")
	if home, err := PrepareHome(requested); home != "" || CodeOf(err) != CodeInvalid {
		t.Fatalf("PrepareHome(intermediate alias) = %q, %v", home, err)
	}
	if _, err := os.Lstat(requested); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aliased home was created: %v", err)
	}
}

func writeLinuxFaultManifest(t *testing.T) (string, string, Manifest) {
	t.Helper()
	home := t.TempDir()
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	manifest := linuxFaultManifest(stateDir)
	if err := writeManifest(stateDir, manifest); err != nil {
		t.Fatal(err)
	}
	return home, stateDir, manifest
}

func linuxFaultManifest(stateDir string) Manifest {
	return Manifest{
		PID: os.Getpid(), Protocol: ProtocolVersion,
		Token: strings.Repeat("a", tokenBytes*2), Endpoint: endpointForStateDirectory(stateDir),
		Epoch: strings.Repeat("b", epochBytes*2),
	}
}

func TestLinuxFaultFilterLeavesOtherThreadsUnaffected(t *testing.T) {
	err := callWithDeniedLinuxSyscalls(t, []uint32{unix.SYS_GETPID}, func() error {
		if os.Getpid() != -1 {
			return errors.New("filtered thread unexpectedly completed getpid")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if os.Getpid() <= 0 {
		t.Fatal("syscall filter escaped its locked test thread")
	}
	if err := context.Background().Err(); err != nil {
		t.Fatal(err)
	}
}
