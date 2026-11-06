package main

import "syscall"

// The app was called Hardware Info before it was Sharktic.
const (
	oldName       = "Hardware Info"
	oldIdentifier = "com.local.hardware-info"
)

const (
	runKey      = `Software\Microsoft\Windows\CurrentVersion\Run`
	approvedKey = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
)

// takeOldLogin removes the entry that started the app under its old name at
// login, which would start the old executable, and tells whether it was on: Task
// Manager turns one off with an odd first byte.
func takeOldLogin() bool {
	if _, ok := regValue(syscall.HKEY_CURRENT_USER, runKey, oldIdentifier); !ok {
		return false
	}
	state, ok := regValue(syscall.HKEY_CURRENT_USER, approvedKey, oldIdentifier)
	on := !ok || len(state) == 0 || state[0]&1 == 0
	deleteRegValue(runKey, oldIdentifier)
	deleteRegValue(approvedKey, oldIdentifier)
	return on
}
