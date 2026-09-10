//go:build unix

//nolint:govet // Independent failure-boundary assertions intentionally reuse narrow error names.
package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const supervisorChildHelperEnvironment = "PICOCLAW_DATABASE_TEST_SUPERVISOR_CHILD"

func init() {
	if os.Getenv(supervisorChildHelperEnvironment) != "1" {
		return
	}
	if err := runSupervisorChildProcessHelper(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "database supervisor child helper: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestConfigureSupervisorProcessUsesPrivateLogAndSession(t *testing.T) {
	home := t.TempDir()
	command := exec.Command("true")
	if err := configureSupervisorProcess(command, home); err != nil {
		t.Fatal(err)
	}
	if command.Stdin != nil || command.Stdout == nil || command.Stdout != command.Stderr ||
		command.SysProcAttr == nil || !command.SysProcAttr.Setsid {
		t.Fatalf("configured supervisor command = %#v", command)
	}
	log, ok := command.Stdout.(*os.File)
	if !ok {
		t.Fatalf("supervisor stdout type = %T", command.Stdout)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(home, "logs", "database-supervisor.log"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("configured log = %#v, %v", info, err)
	}
}

func TestConfigureSupervisorProcessRejectsUnsafeLogBoundary(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "logs"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := configureSupervisorProcess(exec.Command("true"), home); CodeOf(err) != CodeIntegrity {
		t.Fatalf("unsafe supervisor log error = %v", err)
	}
}

func TestUnixSupervisorFileHelpersRejectUnsafePaths(t *testing.T) {
	root := t.TempDir()
	missingParent := filepath.Join(root, "missing", "bootstrap")
	if file, err := createOwnerOnlyExclusiveFile(missingParent, 0o600); err == nil || file != nil {
		t.Fatalf("exclusive file below missing parent = %#v, %v", file, err)
	}
	missing := filepath.Join(root, "missing-log")
	if file, err := openOwnerOnlyAppendFile(missing, 0o600); err == nil || file != nil {
		t.Fatalf("missing append file = %#v, %v", file, err)
	}
	public := filepath.Join(root, "public-log")
	if err := os.WriteFile(public, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if file, err := openOwnerOnlyAppendFile(public, 0o600); CodeOf(err) != CodeIntegrity || file != nil {
		t.Fatalf("public append file = %#v, %v", file, err)
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if file, err := openOwnerOnlyAppendFile(link, 0o600); CodeOf(err) != CodeIntegrity || file != nil {
		t.Fatalf("symlink append file = %#v, %v", file, err)
	}
}

func TestSupervisorExecutableFailsFromRemovedWorkingDirectory(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	removed := filepath.Join(parent, "removed")
	if err := os.Mkdir(removed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(removed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if restoreErr := os.Chdir(original); restoreErr != nil {
			t.Errorf("restore working directory: %v", restoreErr)
		}
	})
	if err := os.Remove(removed); err != nil {
		t.Skipf("platform cannot unlink the working directory: %v", err)
	}
	if err := startSupervisorProcess(EnsureOptions{
		Executable: "./helper", CatalogFingerprint: testCatalogFingerprint,
	}, parent); CodeOf(err) != CodeInvalid {
		t.Fatalf("removed-CWD executable error = %v", err)
	}
}

func TestEnsureSupervisorRealChildReplacesCatalogGeneration(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "catalog-fingerprint")
	firstFingerprint := testCatalogFingerprint
	secondFingerprint := "sha256:" + strings.Repeat("b", 64)
	if err := os.WriteFile(configPath, []byte(firstFingerprint), 0o600); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "picoclaw-supervisor-child")
	currentExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	rawExecutable, err := os.ReadFile(currentExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, rawExecutable, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(helper, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(supervisorChildHelperEnvironment, "1")
	options := EnsureOptions{
		Home: home, Executable: helper, ConfigPath: configPath,
		CatalogFingerprint: firstFingerprint, Timeout: 10 * time.Second,
	}
	first, err := EnsureSupervisor(t.Context(), options)
	if err != nil {
		logData, _ := os.ReadFile(filepath.Join(home, "logs", "database-supervisor.log"))
		t.Fatalf("start first child: %v\n%s", err, logData)
	}
	t.Cleanup(func() {
		client, connectErr := Connect(home)
		if connectErr != nil {
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = client.Shutdown(shutdownCtx)
	})
	firstEpoch := first.Epoch()
	attached, err := EnsureSupervisor(t.Context(), options)
	if err != nil || attached.Epoch() != firstEpoch {
		t.Fatalf("reattach child = %#v, %v", attached, err)
	}
	if writeErr := os.WriteFile(configPath, []byte(secondFingerprint), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	options.CatalogFingerprint = secondFingerprint
	second, err := EnsureSupervisor(t.Context(), options)
	if err != nil {
		logData, _ := os.ReadFile(filepath.Join(home, "logs", "database-supervisor.log"))
		t.Fatalf("replace child: %v\n%s", err, logData)
	}
	if second.Epoch() == firstEpoch {
		t.Fatal("changed catalog generation retained the supervisor epoch")
	}
	status, err := second.Status(t.Context())
	if err != nil || status.CatalogFingerprint != secondFingerprint {
		t.Fatalf("replacement status = %#v, %v", status, err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := second.Shutdown(shutdownCtx); err != nil && CodeOf(err) != CodeOutcomeUnknown {
		t.Fatalf("shutdown replacement child: %v", err)
	}
	waitForUnixProcessExit(t, status.PID)
}

func runSupervisorChildProcessHelper() error {
	home := supervisorTestFlagValue(os.Args, "--home")
	if home == "" || os.Getenv("PICOCLAW_HOME") != home || !ConsumeSupervisorBootstrap(home) {
		return errors.New("child did not receive exact supervisor home/bootstrap authority")
	}
	configPath := os.Getenv("PICOCLAW_CONFIG")
	rawFingerprint, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	fingerprint := strings.TrimSpace(string(rawFingerprint))
	expectedFingerprint := supervisorTestFlagValue(os.Args, "--expected-catalog-fingerprint")
	deadlineValue := supervisorTestFlagValue(os.Args, "--startup-deadline-unix-ns")
	deadlineUnixNS, parseErr := strconv.ParseInt(deadlineValue, 10, 64)
	if !validCatalogFingerprint(fingerprint) || expectedFingerprint != fingerprint ||
		parseErr != nil || !time.Unix(0, deadlineUnixNS).After(time.Now()) {
		return errors.New("child received invalid catalog fingerprint fixture")
	}
	server, err := StartServer(context.Background(), ServerOptions{
		Home: home, CatalogFingerprint: fingerprint,
	})
	if err != nil {
		return err
	}
	<-server.Done()
	return nil
}

func TestUnixSupervisorBootstrapRejectsHardlinkAndSwap(t *testing.T) {
	t.Run("hardlink", func(t *testing.T) {
		home := t.TempDir()
		token := strings.Repeat("c", tokenBytes*2)
		path, err := prepareSupervisorBootstrap(home, token)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Link(path, path+".alias"); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		t.Setenv(supervisorBootstrapEnvironment, token)
		if ConsumeSupervisorBootstrap(home) {
			t.Fatal("hard-linked supervisor bootstrap was consumed")
		}
	})

	t.Run("swap before isolation", func(t *testing.T) {
		home := t.TempDir()
		token := strings.Repeat("d", tokenBytes*2)
		path, err := prepareSupervisorBootstrap(home, token)
		if err != nil {
			t.Fatal(err)
		}
		stateDir, err := StateDirectory(home)
		if err != nil {
			t.Fatal(err)
		}
		stateIdentity, err := supervisorDirectoryIdentity(stateDir)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(path)
		err = consumeSupervisorBootstrapFileWithHook(stateDir, stateIdentity, name, func() {
			if renameErr := os.Rename(path, path+".original"); renameErr != nil {
				t.Errorf("isolate original bootstrap: %v", renameErr)
				return
			}
			if writeErr := os.WriteFile(path, nil, 0o600); writeErr != nil {
				t.Errorf("write replacement bootstrap: %v", writeErr)
			}
		})
		if CodeOf(err) != CodeIntegrity {
			t.Fatalf("swapped supervisor bootstrap error = %v", err)
		}
		if _, statErr := os.Lstat(path + ".original"); statErr != nil {
			t.Fatalf("opened bootstrap identity disappeared: %v", statErr)
		}
		if _, statErr := os.Lstat(path); statErr != nil {
			t.Fatalf("replacement bootstrap was moved or deleted: %v", statErr)
		}
	})

	t.Run("fifo never blocks", func(t *testing.T) {
		home := t.TempDir()
		stateDir, err := prepareStateDirectory(home)
		if err != nil {
			t.Fatal(err)
		}
		token := strings.Repeat("e", tokenBytes*2)
		path := filepath.Join(stateDir, ".bootstrap-"+token)
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Skipf("named pipes unavailable: %v", err)
		}
		t.Setenv(supervisorBootstrapEnvironment, token)
		result := make(chan bool, 1)
		go func() { result <- ConsumeSupervisorBootstrap(home) }()
		select {
		case accepted := <-result:
			if accepted {
				t.Fatal("FIFO supervisor bootstrap was consumed")
			}
		case <-time.After(time.Second):
			t.Fatal("FIFO supervisor bootstrap blocked consumption")
		}
	})

	t.Run("deadline discard never unlinks replacement", func(t *testing.T) {
		home := t.TempDir()
		token := strings.Repeat("f", tokenBytes*2)
		artifact, err := prepareSupervisorBootstrapArtifact(home, token)
		if err != nil {
			t.Fatal(err)
		}
		original := artifact.path + ".original"
		if renameErr := os.Rename(artifact.path, original); renameErr != nil {
			t.Fatal(renameErr)
		}
		if writeErr := os.WriteFile(artifact.path, []byte("replacement"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		if discardErr := discardSupervisorBootstrap(artifact); CodeOf(discardErr) != CodeIntegrity {
			t.Fatalf("swapped bootstrap discard error = %v", discardErr)
		}
		if _, statErr := os.Lstat(original); statErr != nil {
			t.Fatalf("original bootstrap identity disappeared: %v", statErr)
		}
		raw, err := os.ReadFile(artifact.path)
		if err != nil || string(raw) != "replacement" {
			t.Fatalf("replacement was deleted instead of isolated: %q, %v", raw, err)
		}
	})
}

func TestUnixSupervisorProcessGroupDrainProof(t *testing.T) {
	t.Run("eventual absence", func(t *testing.T) {
		probes := 0
		retries := 0
		err := waitForSupervisorProcessGroupExitWith(91, supervisorProcessGroupDrainOps{
			probe: func(processGroupID int) error {
				if processGroupID != 91 {
					t.Fatalf("process group = %d", processGroupID)
				}
				probes++
				if probes == 3 {
					return syscall.ESRCH
				}
				return nil
			},
			retry: func() bool {
				retries++
				return true
			},
		})
		if err != nil || probes != 3 || retries != 2 {
			t.Fatalf("process-group drain = %v, probes=%d retries=%d", err, probes, retries)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		err := waitForSupervisorProcessGroupExitWith(92, supervisorProcessGroupDrainOps{
			probe: func(int) error { return nil },
			retry: func() bool { return false },
		})
		if CodeOf(err) != CodeUnavailable {
			t.Fatalf("process-group drain timeout = %v", err)
		}
	})

	t.Run("probe failure", func(t *testing.T) {
		canary := errors.New("process-group probe failed")
		err := waitForSupervisorProcessGroupExitWith(93, supervisorProcessGroupDrainOps{
			probe: func(int) error { return canary },
			retry: func() bool {
				t.Fatal("probe failure reached retry")
				return false
			},
		})
		if !errors.Is(err, canary) {
			t.Fatalf("process-group probe error = %v", err)
		}
	})

	for _, test := range []struct {
		name string
		id   int
		ops  supervisorProcessGroupDrainOps
	}{
		{name: "invalid group", ops: supervisorProcessGroupDrainOps{
			probe: func(int) error { return syscall.ESRCH }, retry: func() bool { return false },
		}},
		{name: "invalid operations", id: 94},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := waitForSupervisorProcessGroupExitWith(test.id, test.ops); CodeOf(err) != CodeInvalid {
				t.Fatalf("invalid drain inputs = %v", err)
			}
		})
	}
}

func TestUnixSupervisorKillStartsExactWaitThenProvesDrain(t *testing.T) {
	waitStarted := make(chan struct{})
	releaseWait := make(chan struct{})
	done := make(chan struct{})
	signals := make([]syscall.Signal, 0, 1)
	drainCalls := 0
	owner := &unixSupervisorProcessOwner{
		pid: 95,
		wait: func() error {
			close(waitStarted)
			<-releaseWait
			return nil
		},
		done: done,
		signalGroup: func(processGroupID int, signal syscall.Signal) error {
			if processGroupID != 95 {
				t.Fatalf("signaled process group = %d", processGroupID)
			}
			signals = append(signals, signal)
			return nil
		},
		waitGroup: func(processGroupID int) error {
			drainCalls++
			if processGroupID != 95 {
				t.Fatalf("drained process group = %d", processGroupID)
			}
			<-waitStarted
			return nil
		},
	}
	if err := owner.kill(); err != nil {
		t.Fatal(err)
	}
	if len(signals) != 1 || signals[0] != syscall.SIGKILL || drainCalls != 1 {
		t.Fatalf("forced cleanup signals=%v drain calls=%d", signals, drainCalls)
	}
	select {
	case <-done:
		t.Fatal("process waiter completed before the fixture released it")
	default:
	}
	close(releaseWait)
	<-done
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
}

func TestUnixSupervisorOwnerFaultBranches(t *testing.T) {
	var nilOwner *unixSupervisorProcessOwner
	if !nilOwner.exited() || nilOwner.terminate() != nil || nilOwner.kill() != nil || nilOwner.close() != nil {
		t.Fatal("nil Unix supervisor owner was not inert")
	}
	nilOwner.startWait()

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	for _, test := range []struct {
		name    string
		process *os.Process
		wait    func() error
		done    chan struct{}
	}{
		{name: "nil process", wait: func() error { return nil }, done: done},
		{name: "invalid PID", process: &os.Process{}, wait: func() error { return nil }, done: done},
		{name: "nil wait", process: process, done: done},
		{name: "nil done", process: process, wait: func() error { return nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner, ownerErr := newSupervisorProcessOwner(test.process, test.wait, test.done)
			if owner != nil || CodeOf(ownerErr) != CodeIntegrity {
				t.Fatalf("invalid owner = %#v, %v", owner, ownerErr)
			}
		})
	}

	closedDone := make(chan struct{})
	close(closedDone)
	if !(&unixSupervisorProcessOwner{process: process, done: closedDone}).exited() {
		t.Fatal("closed Unix supervisor waiter was reported live")
	}

	t.Run("missing signal operation", func(t *testing.T) {
		owner := &unixSupervisorProcessOwner{pid: 71}
		if err := owner.terminate(); CodeOf(err) != CodeIntegrity {
			t.Fatalf("missing process-group signal = %v", err)
		}
	})

	t.Run("TERM absent group", func(t *testing.T) {
		owner := &unixSupervisorProcessOwner{
			pid:         72,
			signalGroup: func(int, syscall.Signal) error { return syscall.ESRCH },
		}
		if err := owner.terminate(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("TERM signal failure", func(t *testing.T) {
		canary := errors.New("TERM failed")
		owner := &unixSupervisorProcessOwner{
			pid:         73,
			signalGroup: func(int, syscall.Signal) error { return canary },
		}
		if err := owner.terminate(); !errors.Is(err, canary) {
			t.Fatalf("TERM failure = %v", err)
		}
	})

	newWaitOwner := func(
		signal func(int, syscall.Signal) error,
		drain func(int) error,
		waitErr error,
	) (*unixSupervisorProcessOwner, <-chan struct{}) {
		waitDone := make(chan struct{})
		owner := &unixSupervisorProcessOwner{
			pid:         74,
			wait:        func() error { return waitErr },
			done:        waitDone,
			signalGroup: signal,
			waitGroup:   drain,
		}
		return owner, waitDone
	}

	t.Run("missing KILL operations", func(t *testing.T) {
		owner, waitDone := newWaitOwner(nil, nil, nil)
		if err := owner.kill(); CodeOf(err) != CodeIntegrity {
			t.Fatalf("missing KILL operations = %v", err)
		}
		<-waitDone
	})

	t.Run("KILL absent group", func(t *testing.T) {
		drainCalls := 0
		owner, waitDone := newWaitOwner(
			func(int, syscall.Signal) error { return syscall.ESRCH },
			func(int) error { drainCalls++; return nil },
			nil,
		)
		if err := owner.kill(); err != nil {
			t.Fatal(err)
		}
		<-waitDone
		if drainCalls != 0 {
			t.Fatalf("absent process group drain calls = %d", drainCalls)
		}
	})

	t.Run("KILL signal failure", func(t *testing.T) {
		canary := errors.New("KILL failed")
		owner, waitDone := newWaitOwner(
			func(int, syscall.Signal) error { return canary },
			func(int) error { t.Fatal("failed KILL reached drain"); return nil },
			nil,
		)
		if err := owner.kill(); !errors.Is(err, canary) {
			t.Fatalf("KILL failure = %v", err)
		}
		<-waitDone
	})

	t.Run("group drain failure", func(t *testing.T) {
		canary := errors.New("drain failed")
		owner, waitDone := newWaitOwner(
			func(int, syscall.Signal) error { return nil },
			func(int) error { return canary },
			nil,
		)
		if err := owner.kill(); !errors.Is(err, canary) {
			t.Fatalf("group drain failure = %v", err)
		}
		<-waitDone
	})

	t.Run("wait failure retained", func(t *testing.T) {
		canary := errors.New("wait failed")
		owner, waitDone := newWaitOwner(
			func(int, syscall.Signal) error { return syscall.ESRCH },
			func(int) error { return nil },
			canary,
		)
		if err := owner.kill(); err != nil {
			t.Fatal(err)
		}
		<-waitDone
		if err := owner.close(); !errors.Is(err, canary) {
			t.Fatalf("retained Unix wait error = %v", err)
		}
	})

	invalidWaitOwner := &unixSupervisorProcessOwner{pid: 75}
	invalidWaitOwner.startWait()
}

func TestUnixSupervisorLaunchStartFailure(t *testing.T) {
	launch, err := launchSupervisorProcess(exec.Command(filepath.Join(t.TempDir(), "missing")))
	if launch != nil || err == nil {
		t.Fatalf("missing supervisor launch = %#v, %v", launch, err)
	}
}

func TestUnixSupervisorBootstrapFaultBoundaries(t *testing.T) {
	prepare := func(t *testing.T, value string) supervisorBootstrapArtifact {
		t.Helper()
		artifact, err := prepareSupervisorBootstrapArtifact(
			t.TempDir(),
			strings.Repeat(value, tokenBytes*2),
		)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = os.Remove(artifact.path)
			_ = os.Remove(filepath.Join(artifact.stateDir, ".consumed-"+strings.Repeat(value, tokenBytes*2)))
		})
		return artifact
	}

	t.Run("invalid filename", func(t *testing.T) {
		artifact := prepare(t, "1")
		err := consumeSupervisorBootstrapFile(
			artifact.stateDir,
			artifact.stateIdentity,
			".bootstrap-invalid",
			artifact.fileIdentity.String(),
		)
		if CodeOf(err) != CodeInvalid {
			t.Fatalf("invalid bootstrap filename = %v", err)
		}
	})

	t.Run("missing directory", func(t *testing.T) {
		artifact := prepare(t, "2")
		err := consumeSupervisorBootstrapFile(
			filepath.Join(t.TempDir(), "missing"),
			artifact.stateIdentity,
			filepath.Base(artifact.path),
			artifact.fileIdentity.String(),
		)
		if CodeOf(err) != CodeIntegrity {
			t.Fatalf("missing bootstrap directory = %v", err)
		}
	})

	t.Run("changed directory identity", func(t *testing.T) {
		artifact := prepare(t, "3")
		other := prepare(t, "4")
		err := consumeSupervisorBootstrapFile(
			artifact.stateDir,
			other.stateIdentity,
			filepath.Base(artifact.path),
			artifact.fileIdentity.String(),
		)
		if CodeOf(err) != CodeIntegrity {
			t.Fatalf("changed bootstrap directory = %v", err)
		}
	})

	t.Run("unsafe bootstrap mode", func(t *testing.T) {
		artifact := prepare(t, "5")
		if err := os.Chmod(artifact.path, 0o644); err != nil {
			t.Fatal(err)
		}
		err := consumeSupervisorBootstrapFileExpected(
			artifact.stateDir,
			artifact.stateIdentity,
			filepath.Base(artifact.path),
			artifact.fileIdentity,
		)
		if CodeOf(err) != CodeIntegrity {
			t.Fatalf("unsafe bootstrap mode = %v", err)
		}
	})

	t.Run("rename obstruction", func(t *testing.T) {
		artifact := prepare(t, "6")
		consumed := filepath.Join(artifact.stateDir, ".consumed-"+strings.Repeat("6", tokenBytes*2))
		err := consumeSupervisorBootstrapFileExpectedWithHook(
			artifact.stateDir,
			artifact.stateIdentity,
			filepath.Base(artifact.path),
			artifact.fileIdentity,
			artifact.fileIdentity.String(),
			func() {
				if mkdirErr := os.Mkdir(consumed, 0o700); mkdirErr != nil {
					t.Errorf("create consumed-name obstruction: %v", mkdirErr)
				}
			},
		)
		if CodeOf(err) != CodeIntegrity {
			t.Fatalf("obstructed bootstrap rename = %v", err)
		}
	})

	t.Run("nil directory handle", func(t *testing.T) {
		artifact := prepare(t, "7")
		if err := validateSupervisorBootstrapDirectory(
			artifact.stateDir,
			artifact.stateIdentity,
			nil,
		); CodeOf(err) != CodeIntegrity {
			t.Fatalf("nil bootstrap directory = %v", err)
		}
	})

	t.Run("unsafe directory after open", func(t *testing.T) {
		artifact := prepare(t, "8")
		directory, err := os.Open(artifact.stateDir)
		if err != nil {
			t.Fatal(err)
		}
		defer directory.Close()
		if err := os.Chmod(artifact.stateDir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(artifact.stateDir, 0o700) })
		if err := validateSupervisorBootstrapDirectory(
			artifact.stateDir,
			artifact.stateIdentity,
			directory,
		); CodeOf(err) != CodeIntegrity {
			t.Fatalf("unsafe opened bootstrap directory = %v", err)
		}
	})
}

func TestUnixSupervisorFileHelperFaultBranches(t *testing.T) {
	t.Run("exclusive create collision", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bootstrap")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := createOwnerOnlyExclusiveFile(path, 0o600)
		if file != nil || err == nil {
			t.Fatalf("exclusive collision = %#v, %v", file, err)
		}
	})

	t.Run("append create collision", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "log")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := createOwnerOnlyExclusiveAppendFile(path, 0o600)
		if file != nil || err == nil {
			t.Fatalf("append collision = %#v, %v", file, err)
		}
	})

	if links, err := supervisorFileLinkCount(nil); links != 0 || CodeOf(err) != CodeIntegrity {
		t.Fatalf("nil link-count handle = %d, %v", links, err)
	}
	if supervisorTrustedUnixOwner(nil) {
		t.Fatal("nil executable owner was trusted")
	}
	if err := validateSupervisorExecutableTrust("missing", nil); CodeOf(err) != CodeIntegrity {
		t.Fatalf("nil executable trust metadata = %v", err)
	}
	if err := validateSupervisorExecutableAncestorTrust("missing", nil); CodeOf(err) != CodeIntegrity {
		t.Fatalf("nil executable ancestor metadata = %v", err)
	}
}

func TestUnixSupervisorBootstrapInstanceFaults(t *testing.T) {
	prepare := func(t *testing.T, value string) supervisorBootstrapArtifact {
		t.Helper()
		token := strings.Repeat(value, tokenBytes*2)
		artifact, err := prepareSupervisorBootstrapArtifact(t.TempDir(), token)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = os.Remove(artifact.path)
			_ = os.Remove(filepath.Join(artifact.stateDir, ".consumed-"+token))
		})
		return artifact
	}
	defaultOps := func() supervisorUnixBootstrapOps {
		return supervisorUnixBootstrapOps{
			newFile:            os.NewFile,
			validateOpenedFile: validateSupervisorOpenedFile,
			linkCount:          supervisorFileLinkCount,
		}
	}
	consume := func(
		t *testing.T,
		artifact supervisorBootstrapArtifact,
		ops supervisorUnixBootstrapOps,
	) error {
		t.Helper()
		return consumeSupervisorBootstrapFileExpectedWithOps(
			artifact.stateDir,
			artifact.stateIdentity,
			filepath.Base(artifact.path),
			artifact.fileIdentity,
			artifact.fileIdentity.String(),
			nil,
			ops,
		)
	}

	t.Run("directory file construction", func(t *testing.T) {
		artifact := prepare(t, "9")
		ops := defaultOps()
		ops.newFile = func(uintptr, string) *os.File { return nil }
		if err := consume(t, artifact, ops); CodeOf(err) != CodeIntegrity {
			t.Fatalf("nil directory file = %v", err)
		}
	})

	t.Run("bootstrap file construction", func(t *testing.T) {
		artifact := prepare(t, "a")
		ops := defaultOps()
		calls := 0
		ops.newFile = func(descriptor uintptr, name string) *os.File {
			calls++
			if calls == 2 {
				return nil
			}
			return os.NewFile(descriptor, name)
		}
		if err := consume(t, artifact, ops); CodeOf(err) != CodeIntegrity || calls != 2 {
			t.Fatalf("nil bootstrap file = %v, calls=%d", err, calls)
		}
	})

	t.Run("post-isolation validation", func(t *testing.T) {
		artifact := prepare(t, "b")
		ops := defaultOps()
		validations := 0
		ops.validateOpenedFile = func(path string, file *os.File, mode os.FileMode) error {
			validations++
			if validations == 3 {
				return errors.New("post-isolation validation failed")
			}
			return validateSupervisorOpenedFile(path, file, mode)
		}
		if err := consume(t, artifact, ops); CodeOf(err) != CodeIntegrity || validations != 3 {
			t.Fatalf("post-isolation validation = %v, calls=%d", err, validations)
		}
	})

	t.Run("post-unlink link count", func(t *testing.T) {
		artifact := prepare(t, "c")
		ops := defaultOps()
		ops.linkCount = func(*os.File) (int64, error) { return 1, nil }
		if err := consume(t, artifact, ops); CodeOf(err) != CodeIntegrity {
			t.Fatalf("post-unlink link count = %v", err)
		}
	})
}

type supervisorUnixFileInfoWithoutStat struct {
	os.FileInfo
}

func (supervisorUnixFileInfoWithoutStat) Sys() any { return struct{}{} }

func TestUnixSupervisorAppendAndLinkCountInstanceFaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("nofollow loop", func(t *testing.T) {
		file, err := openOwnerOnlyAppendFileWith(path, 0o600, supervisorUnixAppendOpenOps{
			open:    func(string, int, uint32) (int, error) { return -1, unix.ELOOP },
			newFile: os.NewFile,
		})
		if file != nil || CodeOf(err) != CodeIntegrity {
			t.Fatalf("nofollow loop = %#v, %v", file, err)
		}
	})

	t.Run("descriptor construction", func(t *testing.T) {
		file, err := openOwnerOnlyAppendFileWith(path, 0o600, supervisorUnixAppendOpenOps{
			open:    unix.Open,
			newFile: func(uintptr, string) *os.File { return nil },
		})
		if file != nil || CodeOf(err) != CodeIntegrity {
			t.Fatalf("nil append descriptor = %#v, %v", file, err)
		}
	})

	t.Run("unsupported stat metadata", func(t *testing.T) {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		links, err := supervisorFileLinkCountWith(file, func() (os.FileInfo, error) {
			return supervisorUnixFileInfoWithoutStat{FileInfo: info}, nil
		})
		if links != 0 || CodeOf(err) != CodeIntegrity {
			t.Fatalf("unsupported link-count metadata = %d, %v", links, err)
		}
	})
}

type supervisorUnixActivationFaultOwner struct {
	activateErr error
	killed      int
	closed      int
}

func (owner *supervisorUnixActivationFaultOwner) activate() error { return owner.activateErr }
func (*supervisorUnixActivationFaultOwner) exited() bool          { return false }
func (*supervisorUnixActivationFaultOwner) terminate() error      { return nil }
func (owner *supervisorUnixActivationFaultOwner) kill() error {
	owner.killed++
	return nil
}

func (owner *supervisorUnixActivationFaultOwner) close() error {
	owner.closed++
	return nil
}

func TestUnixSupervisorLaunchInstanceFaults(t *testing.T) {
	canary := errors.New("launch ownership failed")
	fakeProcess := &os.Process{Pid: 81}
	t.Run("ownership", func(t *testing.T) {
		kills := 0
		waits := 0
		launch, err := launchSupervisorProcessWith(&exec.Cmd{Process: fakeProcess}, supervisorUnixLaunchOps{
			start: func(*exec.Cmd) error { return nil },
			newOwner: func(*os.Process, func() error, chan struct{}) (supervisorProcessOwner, error) {
				return nil, canary
			},
			kill: func(*os.Process) error { kills++; return nil },
			wait: func(*exec.Cmd) error { waits++; return nil },
		})
		if launch != nil || !errors.Is(err, canary) || kills != 1 || waits != 1 {
			t.Fatalf("ownership failure = %#v, %v, kills=%d waits=%d", launch, err, kills, waits)
		}
	})

	t.Run("activation", func(t *testing.T) {
		owner := &supervisorUnixActivationFaultOwner{activateErr: canary}
		launch, err := launchSupervisorProcessWith(&exec.Cmd{Process: fakeProcess}, supervisorUnixLaunchOps{
			start: func(*exec.Cmd) error { return nil },
			newOwner: func(*os.Process, func() error, chan struct{}) (supervisorProcessOwner, error) {
				return owner, nil
			},
			kill: func(*os.Process) error {
				t.Fatal("activation cleanup bypassed process owner")
				return nil
			},
			wait: func(*exec.Cmd) error {
				t.Fatal("activation cleanup waited outside process owner")
				return nil
			},
		})
		if launch != nil || !errors.Is(err, canary) || owner.killed != 1 || owner.closed != 1 {
			t.Fatalf(
				"activation failure = %#v, %v, killed=%d closed=%d",
				launch,
				err,
				owner.killed,
				owner.closed,
			)
		}
	})
}

func supervisorTestFlagValue(arguments []string, name string) string {
	for index, argument := range arguments {
		if argument == name && index+1 < len(arguments) {
			return strings.TrimSpace(arguments[index+1])
		}
	}
	return ""
}

func waitForUnixProcessExit(t *testing.T, pid int) {
	t.Helper()
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err = process.Signal(syscall.Signal(0))
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("supervisor process %d did not exit: %v", pid, err)
}
