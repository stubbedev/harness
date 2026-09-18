//go:build windows

package procgroup

import (
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func killGroup(proc *os.Process, _ time.Duration) {
	// No signal groups on Windows: kill the direct child. Sessions and
	// the exec handler also assign children to a job object (NewJob)
	// whose kill-on-close takes the whole tree; this remains for
	// callers that never created one.
	_ = proc.Kill()
}

// killHolders is a no-op on Windows: the tty-holder sweep walks /proc
// or shells out to lsof; a closed ConPTY takes its clients with it.
func killHolders(string) {}

// NewJob puts proc in a fresh job object configured with
// kill-on-close: every descendant spawned after the assignment joins
// the job, and closing (or terminating) the job kills them all - the
// Windows answer to "a grandchild must not survive teardown". Assign
// immediately after spawn, before the child can spawn anything itself:
// processes created before the assignment are outside the job.
func NewJob(proc *os.Process) uintptr {
	// os.Process hides its handle, so open the pid ourselves with the
	// rights the job calls need.
	const rights = windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE
	ph, err := windows.OpenProcess(rights, false, uint32(proc.Pid))
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(ph)

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return 0
	}
	if err := windows.AssignProcessToJobObject(job, ph); err != nil {
		_ = windows.CloseHandle(job)
		return 0
	}
	return uintptr(job)
}

// TerminateJob kills every process in the job at once.
func TerminateJob(job uintptr) {
	if job == 0 {
		return
	}
	_ = windows.TerminateJobObject(windows.Handle(job), 1)
}

// CloseJob releases the job handle; with kill-on-close configured,
// that alone terminates any surviving members.
func CloseJob(job uintptr) {
	if job == 0 {
		return
	}
	_ = windows.CloseHandle(windows.Handle(job))
}
