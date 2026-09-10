//go:build windows

package database

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	detachedProcess                  = 0x00000008
	createNewProcessGroup            = 0x00000200
	procThreadAttributeJobList       = 0x0002000d
	windowsSupervisorInfiniteTimeout = 0xffffffff
)

type windowsSupervisorLaunchOps struct {
	createJob          func() (windows.Handle, error)
	setKillOnJobClose  func(windows.Handle, bool) error
	queryJobProcesses  func(windows.Handle) (uint32, error)
	createProcessInJob func(*exec.Cmd, windows.Handle) (windows.ProcessInformation, error)
	findProcess        func(int) (*os.Process, error)
	resumeThread       func(windows.Handle) (uint32, error)
	waitForProcess     func(windows.Handle, uint32) (uint32, error)
	terminateJob       func(windows.Handle, uint32) error
	closeHandle        func(windows.Handle) error
}

func configureSupervisorProcess(command *exec.Cmd, home string) error {
	return configureSupervisorLog(command, home)
}

func launchSupervisorProcess(command *exec.Cmd) (*supervisorProcessLaunch, error) {
	return launchWindowsSupervisorProcess(command, windowsSupervisorLaunchOps{
		createJob: func() (windows.Handle, error) {
			return windows.CreateJobObject(nil, nil)
		},
		setKillOnJobClose:  setWindowsSupervisorJobKillOnClose,
		queryJobProcesses:  queryWindowsSupervisorJobActiveProcesses,
		createProcessInJob: createWindowsSupervisorProcessInJob,
		findProcess:        os.FindProcess,
		resumeThread:       windows.ResumeThread,
		waitForProcess:     windows.WaitForSingleObject,
		terminateJob:       windows.TerminateJobObject,
		closeHandle:        windows.CloseHandle,
	})
}

func launchWindowsSupervisorProcess(
	command *exec.Cmd,
	ops windowsSupervisorLaunchOps,
) (*supervisorProcessLaunch, error) {
	if command == nil || ops.createJob == nil || ops.setKillOnJobClose == nil ||
		ops.queryJobProcesses == nil ||
		ops.createProcessInJob == nil ||
		ops.findProcess == nil || ops.resumeThread == nil || ops.waitForProcess == nil ||
		ops.terminateJob == nil || ops.closeHandle == nil {
		return nil, NewError(CodeInvalid, "database supervisor Windows launch operations are invalid")
	}
	job, err := ops.createJob()
	if err != nil {
		return nil, err
	}
	if job == 0 || job == windows.InvalidHandle {
		return nil, NewError(CodeIntegrity, "database supervisor job is unavailable")
	}
	if err := ops.setKillOnJobClose(job, true); err != nil {
		return nil, errors.Join(err, ops.closeHandle(job))
	}
	processInfo, err := ops.createProcessInJob(command, job)
	if err != nil {
		return nil, errors.Join(err, closeWindowsSupervisorProcessInformation(processInfo, ops), ops.closeHandle(job))
	}
	if processInfo.Process == 0 || processInfo.Thread == 0 || processInfo.ProcessId == 0 {
		return nil, errors.Join(
			NewError(CodeIntegrity, "database supervisor process information is invalid"),
			ops.terminateJob(job, 1),
			closeWindowsSupervisorProcessInformation(processInfo, ops),
			ops.closeHandle(job),
		)
	}
	process, err := ops.findProcess(int(processInfo.ProcessId))
	if err != nil {
		return nil, errors.Join(
			err,
			ops.terminateJob(job, 1),
			closeWindowsSupervisorProcessInformation(processInfo, ops),
			ops.closeHandle(job),
		)
	}
	done := make(chan struct{})
	owner := &windowsSupervisorProcessOwner{
		job: job, processHandle: processInfo.Process, thread: processInfo.Thread,
		process: process, done: done,
		resumeThread: ops.resumeThread, waitForProcess: ops.waitForProcess,
		terminateJob: ops.terminateJob, queryJobProcesses: ops.queryJobProcesses,
		setKillOnJobClose: ops.setKillOnJobClose,
		closeHandle:       ops.closeHandle,
	}
	if err := owner.activate(); err != nil {
		return nil, errors.Join(err, owner.kill(), owner.close())
	}
	return &supervisorProcessLaunch{
		pid: int(processInfo.ProcessId), owner: owner, done: done,
	}, nil
}

type windowsSupervisorJobAccountingInformation struct {
	totalUserTime             int64
	totalKernelTime           int64
	thisPeriodTotalUserTime   int64
	thisPeriodTotalKernelTime int64
	totalPageFaultCount       uint32
	totalProcesses            uint32
	activeProcesses           uint32
	totalTerminatedProcesses  uint32
}

func queryWindowsSupervisorJobActiveProcesses(job windows.Handle) (uint32, error) {
	if job == 0 || job == windows.InvalidHandle {
		return 0, NewError(CodeInvalid, "database supervisor Windows job is invalid")
	}
	information := windowsSupervisorJobAccountingInformation{}
	err := windows.QueryInformationJobObject(
		job,
		windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
		nil,
	)
	runtime.KeepAlive(&information)
	return information.activeProcesses, err
}

func setWindowsSupervisorJobKillOnClose(job windows.Handle, enabled bool) error {
	if job == 0 || job == windows.InvalidHandle {
		return NewError(CodeInvalid, "database supervisor Windows job is invalid")
	}
	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	if enabled {
		information.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	}
	_, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
	)
	runtime.KeepAlive(&information)
	return err
}

func closeWindowsSupervisorProcessInformation(
	information windows.ProcessInformation,
	ops windowsSupervisorLaunchOps,
) error {
	var result error
	if information.Thread != 0 && ops.closeHandle != nil {
		result = errors.Join(result, ops.closeHandle(information.Thread))
	}
	if information.Process != 0 && ops.closeHandle != nil {
		result = errors.Join(result, ops.closeHandle(information.Process))
	}
	return result
}

// createWindowsSupervisorProcessInJob uses the Job List startup attribute so
// Windows assigns the new process to the Job atomically, before its primary
// thread can execute. ProcessInformation supplies that exact primary thread
// handle to the caller for the one and only ResumeThread operation.
func createWindowsSupervisorProcessInJob(
	command *exec.Cmd,
	job windows.Handle,
) (windows.ProcessInformation, error) {
	if command == nil || command.Path == "" || job == 0 {
		return windows.ProcessInformation{}, NewError(CodeInvalid, "database supervisor Windows command is invalid")
	}
	args := command.Args
	if len(args) == 0 {
		args = []string{command.Path}
	}
	application, err := windows.UTF16PtrFromString(command.Path)
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	environment, err := windowsSupervisorEnvironmentBlock(command.Environ())
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	var currentDirectory *uint16
	if command.Dir != "" {
		currentDirectory, err = windows.UTF16PtrFromString(command.Dir)
		if err != nil {
			return windows.ProcessInformation{}, err
		}
	}

	nullFile, err := os.Open(os.DevNull)
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	defer nullFile.Close()
	stdin, err := windowsSupervisorFile(command.Stdin, nullFile)
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	stdout, err := windowsSupervisorFile(command.Stdout, nullFile)
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	stderr, err := windowsSupervisorFile(command.Stderr, nullFile)
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	inherited := make([]windows.Handle, 0, 3)
	for _, file := range []*os.File{stdin, stdout, stderr} {
		handle, duplicateErr := duplicateWindowsSupervisorHandle(file)
		if duplicateErr != nil {
			for _, inheritedHandle := range inherited {
				_ = windows.CloseHandle(inheritedHandle)
			}
			return windows.ProcessInformation{}, duplicateErr
		}
		inherited = append(inherited, handle)
	}
	defer func() {
		for _, inheritedHandle := range inherited {
			_ = windows.CloseHandle(inheritedHandle)
		}
	}()

	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	defer attributes.Delete()
	err = attributes.Update(
		procThreadAttributeJobList, unsafe.Pointer(&job), unsafe.Sizeof(job),
	)
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	err = attributes.Update(
		windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
		unsafe.Pointer(&inherited[0]),
		uintptr(len(inherited))*unsafe.Sizeof(inherited[0]),
	)
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	startupInfo := windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:         uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags:      windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW,
			ShowWindow: windows.SW_HIDE,
			StdInput:   inherited[0],
			StdOutput:  inherited[1],
			StdErr:     inherited[2],
		},
		ProcThreadAttributeList: attributes.List(),
	}
	var processInfo windows.ProcessInformation
	err = windows.CreateProcess(
		application,
		commandLine,
		nil,
		nil,
		true,
		detachedProcess|createNewProcessGroup|windows.CREATE_SUSPENDED|
			windows.CREATE_UNICODE_ENVIRONMENT|windows.EXTENDED_STARTUPINFO_PRESENT,
		&environment[0],
		currentDirectory,
		&startupInfo.StartupInfo,
		&processInfo,
	)
	runtime.KeepAlive(job)
	runtime.KeepAlive(inherited)
	return processInfo, err
}

func windowsSupervisorFile(stream any, nullFile *os.File) (*os.File, error) {
	if stream == nil {
		return nullFile, nil
	}
	file, ok := stream.(*os.File)
	if !ok || file == nil {
		return nil, errors.New("database supervisor Windows standard stream is not a file")
	}
	return file, nil
}

func duplicateWindowsSupervisorHandle(file *os.File) (windows.Handle, error) {
	if file == nil {
		return 0, NewError(CodeIntegrity, "database supervisor Windows standard stream is unavailable")
	}
	currentProcess := windows.CurrentProcess()
	var duplicate windows.Handle
	err := windows.DuplicateHandle(
		currentProcess,
		windows.Handle(file.Fd()),
		currentProcess,
		&duplicate,
		0,
		true,
		windows.DUPLICATE_SAME_ACCESS,
	)
	return duplicate, err
}

func windowsSupervisorEnvironmentBlock(environment []string) ([]uint16, error) {
	if environment == nil {
		environment = os.Environ()
	}
	if len(environment) == 0 {
		return []uint16{0, 0}, nil
	}
	result := make([]uint16, 0)
	for _, entry := range environment {
		if strings.IndexByte(entry, 0) >= 0 {
			return nil, errors.New("database supervisor Windows environment contains NUL")
		}
		for _, character := range entry {
			result = utf16.AppendRune(result, character)
		}
		result = append(result, 0)
	}
	return append(result, 0), nil
}
