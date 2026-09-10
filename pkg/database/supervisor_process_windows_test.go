//go:build windows

package database

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsSupervisorLaunchUsesJobAtCreateAndExactPrimaryThread(t *testing.T) {
	const (
		jobHandle     = windows.Handle(101)
		processHandle = windows.Handle(202)
		threadHandle  = windows.Handle(303)
	)
	var mu sync.Mutex
	events := make([]string, 0, 8)
	record := func(event string) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}
	waitRelease := make(chan struct{})
	launch, err := launchWindowsSupervisorProcess(exec.Command("unused.exe"), windowsSupervisorLaunchOps{
		createJob: func() (windows.Handle, error) {
			record("create-job")
			return jobHandle, nil
		},
		setKillOnJobClose: func(job windows.Handle, enabled bool) error {
			record(fmt.Sprintf("kill-on-close:%d:%t", job, enabled))
			return nil
		},
		queryJobProcesses: func(windows.Handle) (uint32, error) { return 0, nil },
		createProcessInJob: func(_ *exec.Cmd, job windows.Handle) (windows.ProcessInformation, error) {
			record(fmt.Sprintf("create-in-job:%d", job))
			if job != jobHandle {
				return windows.ProcessInformation{}, fmt.Errorf("job = %d", job)
			}
			return windows.ProcessInformation{
				Process: processHandle, Thread: threadHandle, ProcessId: uint32(os.Getpid()),
			}, nil
		},
		findProcess: func(pid int) (*os.Process, error) {
			record(fmt.Sprintf("find:%d", pid))
			return os.FindProcess(pid)
		},
		resumeThread: func(thread windows.Handle) (uint32, error) {
			record(fmt.Sprintf("resume:%d", thread))
			if thread != threadHandle {
				return 0, fmt.Errorf("resumed thread = %d", thread)
			}
			return 1, nil
		},
		waitForProcess: func(process windows.Handle, _ uint32) (uint32, error) {
			record(fmt.Sprintf("wait:%d", process))
			<-waitRelease
			return windows.WAIT_OBJECT_0, nil
		},
		terminateJob: func(job windows.Handle, _ uint32) error {
			record(fmt.Sprintf("terminate:%d", job))
			return nil
		},
		closeHandle: func(handle windows.Handle) error {
			record(fmt.Sprintf("close:%d", handle))
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	prefix := append([]string(nil), events...)
	mu.Unlock()
	wantPrefix := []string{
		"create-job",
		"kill-on-close:101:true",
		"create-in-job:101",
		fmt.Sprintf("find:%d", os.Getpid()),
		"resume:303",
		"close:303",
	}
	if len(prefix) < len(wantPrefix) {
		t.Fatalf("launch events = %v, want prefix %v", prefix, wantPrefix)
	}
	for index, want := range wantPrefix {
		if prefix[index] != want {
			t.Fatalf("launch events = %v, want prefix %v", prefix, wantPrefix)
		}
	}
	if err := launch.owner.close(); err != nil {
		t.Fatal(err)
	}
	close(waitRelease)
	<-launch.done
	if err := launch.owner.close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !containsWindowsLaunchEvent(events, "close:202") ||
		!containsWindowsLaunchEvent(events, "close:101") ||
		!containsWindowsLaunchEvent(events, "kill-on-close:101:false") {
		t.Fatalf("exact process/job handles were not closed: %v", events)
	}
}

func containsWindowsLaunchEvent(events []string, want string) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}

func TestWindowsSupervisorStartsAtomicallyInJob(t *testing.T) {
	command := exec.Command("cmd.exe", "/c", "exit", "0")
	if err := configureSupervisorProcess(command, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	logFile, _ := command.Stdout.(*os.File)
	launch, err := launchSupervisorProcess(command)
	if logFile != nil {
		_ = logFile.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := launch.owner.close(); err != nil {
		t.Fatal(err)
	}
	<-launch.done
}

func TestWindowsSupervisorInvalidResumeStateRetainsJobContainment(t *testing.T) {
	const (
		jobHandle     = windows.Handle(111)
		processHandle = windows.Handle(222)
		threadHandle  = windows.Handle(333)
	)
	var mu sync.Mutex
	events := make([]string, 0, 10)
	record := func(event string) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}
	processClosed := make(chan struct{})
	var processCloseOnce sync.Once
	_, err := launchWindowsSupervisorProcess(exec.Command("unused.exe"), windowsSupervisorLaunchOps{
		createJob: func() (windows.Handle, error) { return jobHandle, nil },
		setKillOnJobClose: func(job windows.Handle, enabled bool) error {
			record(fmt.Sprintf("kill-on-close:%d:%t", job, enabled))
			return nil
		},
		queryJobProcesses: func(windows.Handle) (uint32, error) { return 0, nil },
		createProcessInJob: func(*exec.Cmd, windows.Handle) (windows.ProcessInformation, error) {
			return windows.ProcessInformation{
				Process: processHandle, Thread: threadHandle, ProcessId: uint32(os.Getpid()),
			}, nil
		},
		findProcess: os.FindProcess,
		resumeThread: func(windows.Handle) (uint32, error) {
			record("resume")
			return 0, nil
		},
		waitForProcess: func(windows.Handle, uint32) (uint32, error) {
			record("wait")
			return windows.WAIT_OBJECT_0, nil
		},
		terminateJob: func(job windows.Handle, _ uint32) error {
			record(fmt.Sprintf("terminate:%d", job))
			return nil
		},
		closeHandle: func(handle windows.Handle) error {
			record(fmt.Sprintf("close:%d", handle))
			if handle == processHandle {
				processCloseOnce.Do(func() { close(processClosed) })
			}
			return nil
		},
	})
	if CodeOf(err) != CodeIntegrity {
		t.Fatalf("invalid ResumeThread count error = %v", err)
	}
	<-processClosed
	mu.Lock()
	defer mu.Unlock()
	for _, unexpected := range []string{"kill-on-close:111:false"} {
		if containsWindowsLaunchEvent(events, unexpected) {
			t.Fatalf("invalid launch detached its containment: %v", events)
		}
	}
	for _, expected := range []string{
		"kill-on-close:111:true", "resume", "close:333", "terminate:111", "close:111", "wait", "close:222",
	} {
		if !containsWindowsLaunchEvent(events, expected) {
			t.Fatalf("invalid launch missed %q: %v", expected, events)
		}
	}
}

func TestWindowsSupervisorWaitFailureIsRetained(t *testing.T) {
	const (
		jobHandle     = windows.Handle(121)
		processHandle = windows.Handle(242)
		threadHandle  = windows.Handle(363)
	)
	canary := errors.New("wait failed")
	waits := 0
	launch, err := launchWindowsSupervisorProcess(exec.Command("unused.exe"), windowsSupervisorLaunchOps{
		createJob:         func() (windows.Handle, error) { return jobHandle, nil },
		setKillOnJobClose: func(windows.Handle, bool) error { return nil },
		queryJobProcesses: func(windows.Handle) (uint32, error) { return 0, nil },
		createProcessInJob: func(*exec.Cmd, windows.Handle) (windows.ProcessInformation, error) {
			return windows.ProcessInformation{
				Process: processHandle, Thread: threadHandle, ProcessId: uint32(os.Getpid()),
			}, nil
		},
		findProcess:  os.FindProcess,
		resumeThread: func(windows.Handle) (uint32, error) { return 1, nil },
		waitForProcess: func(windows.Handle, uint32) (uint32, error) {
			waits++
			return windows.WAIT_FAILED, canary
		},
		terminateJob: func(windows.Handle, uint32) error { return nil },
		closeHandle:  func(windows.Handle) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	firstCloseErr := launch.owner.close()
	<-launch.done
	secondCloseErr := launch.owner.close()
	if err := errors.Join(firstCloseErr, secondCloseErr); !errors.Is(err, canary) {
		t.Fatalf("retained process wait error = %v", err)
	}
	if waits != 1 {
		t.Fatalf("process wait calls = %d, want 1", waits)
	}
}

func TestWindowsSupervisorFailedAttemptNeverClearsKillOnClose(t *testing.T) {
	const (
		jobHandle     = windows.Handle(131)
		processHandle = windows.Handle(262)
		threadHandle  = windows.Handle(393)
	)
	var mu sync.Mutex
	settings := make([]bool, 0, 2)
	terminations := 0
	launch, err := launchWindowsSupervisorProcess(exec.Command("unused.exe"), windowsSupervisorLaunchOps{
		createJob: func() (windows.Handle, error) { return jobHandle, nil },
		setKillOnJobClose: func(_ windows.Handle, boolValue bool) error {
			mu.Lock()
			settings = append(settings, boolValue)
			mu.Unlock()
			return nil
		},
		queryJobProcesses: func(windows.Handle) (uint32, error) { return 0, nil },
		createProcessInJob: func(*exec.Cmd, windows.Handle) (windows.ProcessInformation, error) {
			return windows.ProcessInformation{
				Process: processHandle, Thread: threadHandle, ProcessId: uint32(os.Getpid()),
			}, nil
		},
		findProcess:  os.FindProcess,
		resumeThread: func(windows.Handle) (uint32, error) { return 1, nil },
		waitForProcess: func(windows.Handle, uint32) (uint32, error) {
			return windows.WAIT_OBJECT_0, nil
		},
		terminateJob: func(windows.Handle, uint32) error {
			terminations++
			return nil
		},
		closeHandle: func(windows.Handle) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := launch.owner.kill(); err != nil {
		t.Fatal(err)
	}
	<-launch.done
	if err := launch.owner.close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(settings) != 1 || !settings[0] || terminations != 1 {
		t.Fatalf("failed-attempt job settings=%v terminations=%d", settings, terminations)
	}
}

func TestWindowsSupervisorJobDrainProof(t *testing.T) {
	const job = windows.Handle(414)
	t.Run("eventual empty job", func(t *testing.T) {
		active := []uint32{3, 1, 0}
		queries := 0
		retries := 0
		err := waitForWindowsSupervisorJobDrainWith(job, windowsSupervisorJobDrainOps{
			query: func(candidate windows.Handle) (uint32, error) {
				if candidate != job {
					t.Fatalf("queried job = %d", candidate)
				}
				result := active[queries]
				queries++
				return result, nil
			},
			retry: func() bool {
				retries++
				return true
			},
		})
		if err != nil || queries != 3 || retries != 2 {
			t.Fatalf("job drain = %v, queries=%d retries=%d", err, queries, retries)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		err := waitForWindowsSupervisorJobDrainWith(job, windowsSupervisorJobDrainOps{
			query: func(windows.Handle) (uint32, error) { return 1, nil },
			retry: func() bool { return false },
		})
		if CodeOf(err) != CodeUnavailable {
			t.Fatalf("job drain timeout = %v", err)
		}
	})

	t.Run("query failure", func(t *testing.T) {
		canary := errors.New("query job failed")
		err := waitForWindowsSupervisorJobDrainWith(job, windowsSupervisorJobDrainOps{
			query: func(windows.Handle) (uint32, error) { return 0, canary },
			retry: func() bool {
				t.Fatal("query failure reached retry")
				return false
			},
		})
		if !errors.Is(err, canary) {
			t.Fatalf("job query error = %v", err)
		}
	})

	for _, test := range []struct {
		name string
		job  windows.Handle
		ops  windowsSupervisorJobDrainOps
	}{
		{name: "invalid job", ops: windowsSupervisorJobDrainOps{
			query: func(windows.Handle) (uint32, error) { return 0, nil },
			retry: func() bool { return false },
		}},
		{name: "invalid operations", job: job},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := waitForWindowsSupervisorJobDrainWith(test.job, test.ops); CodeOf(err) != CodeInvalid {
				t.Fatalf("invalid job drain = %v", err)
			}
		})
	}
}
