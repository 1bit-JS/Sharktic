package main

import (
	"syscall"
	"unsafe"
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")

	procFindWindowEx       = user32.NewProc("FindWindowExW")
	procGetClientRect      = user32.NewProc("GetClientRect")
	procSendMessageTimeout = user32.NewProc("SendMessageTimeoutW")
)

const (
	wmMouseMove     = 0x0200
	smtoAbortIfHung = 0x0002
)

// findWindowPath follows a path of window classes down from the desktop, and
// returns 0 if a window on the way is missing.
func findWindowPath(classes ...string) uintptr {
	hwnd := uintptr(0)
	for _, class := range classes {
		c, _ := syscall.UTF16PtrFromString(class)
		hwnd, _, _ = procFindWindowEx.Call(hwnd, 0, uintptr(unsafe.Pointer(c)), 0)
		if hwnd == 0 {
			return 0
		}
	}
	return hwnd
}

// clearDeadTrayIcons removes the icons that killed processes left behind, ours
// among them. The taskbar keeps those until the mouse passes over them, so this
// tells it that the mouse did, without moving it. On a Windows with another
// taskbar it finds no windows and does nothing.
func clearDeadTrayIcons() {
	for _, toolbar := range []uintptr{
		findWindowPath("Shell_TrayWnd", "TrayNotifyWnd", "SysPager", "ToolbarWindow32"),
		findWindowPath("NotifyIconOverflowWindow", "ToolbarWindow32"),
	} {
		if toolbar == 0 {
			continue
		}
		var r struct{ left, top, right, bottom int32 }
		if ok, _, _ := procGetClientRect.Call(toolbar, uintptr(unsafe.Pointer(&r))); ok == 0 {
			continue
		}
		// Finer than the smallest icon, which is 16 pixels.
		const step = 8
		for y := r.top; y < r.bottom; y += step {
			for x := r.left; x < r.right; x += step {
				var result uintptr
				lParam := uintptr(uint32(y)<<16 | uint32(x)&0xFFFF)
				procSendMessageTimeout.Call(toolbar, wmMouseMove, 0, lParam, smtoAbortIfHung, 100, uintptr(unsafe.Pointer(&result)))
			}
		}
	}
}
