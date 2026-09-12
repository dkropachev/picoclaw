//go:build linux

package sqliteprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type stagedRetirementSeccompResult struct {
	err      error
	setupErr error
}

func TestStagedRetirementLinuxSyscallFailureCoverage(t *testing.T) {
	t.Run("retained parent fstat", func(t *testing.T) {
		stage := writeStagedRetirementFile(t, t.TempDir(), "stage.db", "retained")
		result := runStagedRetirementWithDeniedSyscall(unix.SYS_FSTAT, func() error {
			retained, err := retainStagedGenerationPlatform(stage)
			if retained != nil {
				err = errors.Join(err, retained.close())
			}
			return err
		})
		if result.setupErr != nil {
			t.Skipf("seccomp unavailable: %v", result.setupErr)
		}
		if result.err == nil {
			t.Fatal("retainStagedGenerationPlatform() succeeded with fstat denied")
		}
		assertStagedRetirementContents(t, stage, "retained")
	})

	t.Run("unlink", func(t *testing.T) {
		root := t.TempDir()
		stage := writeStagedRetirementFile(t, root, "stage.db", "retained")
		retained, err := retainStagedGeneration(context.Background(), stage)
		if err != nil {
			t.Fatalf("retainStagedGeneration() error = %v", err)
		}
		defer func() {
			if retained.platform != nil && retained.platform.quarantine != "" {
				quarantine := filepath.Join(root, retained.platform.quarantine)
				if _, statErr := os.Lstat(stage); errors.Is(statErr, os.ErrNotExist) {
					_ = os.Rename(quarantine, stage)
				}
			}
			if closeErr := retained.Close(); closeErr != nil {
				t.Errorf("Close() error = %v", closeErr)
			}
		}()
		result := runStagedRetirementWithDeniedSyscall(unix.SYS_UNLINKAT, func() error {
			return retained.Retire(context.Background())
		})
		if result.setupErr != nil {
			t.Skipf("seccomp unavailable: %v", result.setupErr)
		}
		if result.err == nil {
			t.Fatal("Retire() succeeded with unlinkat denied")
		}
		if retained.platform.quarantine == "" {
			t.Fatal("Retire() did not retain the quarantined exact stage")
		}
		if _, statErr := os.Lstat(stage); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("original stage after denied unlinkat = %v", statErr)
		}
		assertStagedRetirementContents(
			t,
			filepath.Join(root, retained.platform.quarantine),
			"retained",
		)
	})
}

func runStagedRetirementWithDeniedSyscall(
	syscallNumber uintptr,
	operation func() error,
) stagedRetirementSeccompResult {
	result := make(chan stagedRetirementSeccompResult, 1)
	go func() {
		// A seccomp filter cannot be removed. Leaving this goroutine locked when
		// it returns makes the runtime retire this OS thread instead of reusing a
		// filtered thread for another test.
		runtime.LockOSThread()
		if err := installStagedRetirementSeccompDenial(syscallNumber); err != nil {
			result <- stagedRetirementSeccompResult{setupErr: err}
			return
		}
		result <- stagedRetirementSeccompResult{err: operation()}
	}()
	select {
	case completed := <-result:
		return completed
	case <-time.After(10 * time.Second):
		return stagedRetirementSeccompResult{setupErr: errors.New("seccomp test timed out")}
	}
}

func installStagedRetirementSeccompDenial(syscallNumber uintptr) error {
	filter := []unix.SockFilter{
		{
			Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS,
			K:    0, // offsetof(struct seccomp_data, nr)
		},
		{
			Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K,
			Jt:   0,
			Jf:   1,
			K:    uint32(syscallNumber),
		},
		{
			Code: unix.BPF_RET | unix.BPF_K,
			K:    unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM),
		},
		{
			Code: unix.BPF_RET | unix.BPF_K,
			K:    unix.SECCOMP_RET_ALLOW,
		},
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	if err := unix.Prctl(
		unix.PR_SET_SECCOMP,
		unix.SECCOMP_MODE_FILTER,
		uintptr(unsafe.Pointer(&program)),
		0,
		0,
	); err != nil {
		return err
	}
	runtime.KeepAlive(filter)
	return nil
}
