package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// killChildrenOnExit puts the test process into a Job Object that kills
// every process in it once its last handle closes. Children join their
// parent's job, so the dash servers the end-to-end tests start stop when
// the test process dies, even when it is killed: Windows leaves a dead
// parent's children running, and servers left over from killed runs
// slowed later runs into timeouts.
//
// Only this process holds the job's handle (it isn't inherited), so the
// job closes, and the servers stop, as this process exits.
func killChildrenOnExit() error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("set job object limits: %w", err)
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("join job object: %w", err)
	}
	return nil // the handle stays open until the process exits
}
