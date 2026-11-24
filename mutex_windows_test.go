package main

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// heldElsewhere tells whether a thread other than the caller's holds mu.
func heldElsewhere(t *testing.T, mu windows.Handle) bool {
	ch := make(chan bool)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		ev, err := windows.WaitForSingleObject(mu, 0)
		if err != nil {
			t.Error(err)
		}
		if ev == windows.WAIT_OBJECT_0 || ev == windows.WAIT_ABANDONED {
			windows.ReleaseMutex(mu)
			ch <- false
			return
		}
		ch <- true
	}()
	return <-ch
}

func TestWithGlobalMutexReleases(t *testing.T) {
	mu, err := windows.CreateMutex(nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(mu)
	// Many goroutines, which Go moves between threads, as the service's do.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				err := withGlobalMutex(mu, time.Second, func() error {
					runtime.Gosched()
					time.Sleep(10 * time.Microsecond)
					return nil
				})
				if err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if heldElsewhere(t, mu) {
		t.Fatal("the mutex is still held")
	}
}

func TestOpenGlobalMutexMadeByAnother(t *testing.T) {
	// Another program made it first, as HWiNFO does.
	name := `Local\SharkticTest-` + time.Now().Format("150405.000000000")
	first, err := openGlobalMutex(name)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(first)
	second, err := openGlobalMutex(name)
	if err != nil {
		t.Fatalf("opening an existing mutex: %v", err)
	}
	defer windows.CloseHandle(second)
	if err := withGlobalMutex(second, time.Second, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
