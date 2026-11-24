package main

import (
	"errors"
	"fmt"
	"runtime"
	"time"

	"golang.org/x/sys/windows"
)

// withGlobalMutex runs fn while holding a mutex that other hardware programs,
// as HWiNFO and LibreHardwareMonitor, share with the app, or tells that it is
// busy. A Windows mutex belongs to the thread that took it, and only that
// thread can release it: Go moves a goroutine between threads, so fn runs
// on one thread throughout. Released by another, the mutex would stay held
// until the service stops, and keep the other programs waiting.
func withGlobalMutex(mu windows.Handle, wait time.Duration, fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ev, err := windows.WaitForSingleObject(mu, uint32(wait/time.Millisecond))
	switch {
	case err != nil:
		return err
	case ev == uint32(windows.WAIT_TIMEOUT):
		return errors.New("busy")
	case ev != windows.WAIT_OBJECT_0 && ev != windows.WAIT_ABANDONED:
		return fmt.Errorf("wait: %#x", ev)
	}
	// WAIT_ABANDONED: a program quit while holding it, which leaves it ours.
	err = fn()
	if rerr := windows.ReleaseMutex(mu); rerr != nil && err == nil {
		err = fmt.Errorf("release: %w", rerr)
	}
	return err
}

// openGlobalMutex opens a shared mutex by name, making it if no program has
// yet. CreateMutex reports ERROR_ALREADY_EXISTS when another program, as
// HWiNFO, made it first, with a good handle: that is the usual case.
func openGlobalMutex(name string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	mu, err := windows.CreateMutex(nil, false, p)
	if mu != 0 && errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		err = nil
	}
	if err != nil {
		if mu != 0 {
			windows.CloseHandle(mu)
		}
		return 0, fmt.Errorf("mutex %s: %w", name, err)
	}
	return mu, nil
}
