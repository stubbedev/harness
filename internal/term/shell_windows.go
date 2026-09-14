package term

import (
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// parentProcessName returns the executable name of the process that
// launched Harness, or "" when it cannot be read. Windows has no parent
// pointer on a process handle, so the answer comes from walking the
// process table for this process's parent entry.
func parentProcessName() string {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(snapshot) //nolint:errcheck

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return ""
	}
	self := uint32(os.Getpid())
	var parentPID uint32
	for {
		if entry.ProcessID == self {
			parentPID = entry.ParentProcessID
			break
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			return ""
		}
	}
	if parentPID == 0 {
		return ""
	}

	// The table is a snapshot, so the parent's entry is in the one we
	// already hold; a second walk avoids re-snapshotting a table that
	// may have changed underneath us.
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return ""
	}
	for {
		if entry.ProcessID == parentPID {
			return filepath.Base(windows.UTF16ToString(entry.ExeFile[:]))
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			return ""
		}
	}
}
