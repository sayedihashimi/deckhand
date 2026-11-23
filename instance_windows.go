package main

import (
	"os"
	"runtime"

	"golang.org/x/sys/windows"
)

// acquireMainLock takes the main instance's named mutex (mainLockName) and
// holds it for the life of the process. It reports false when another
// process holds it.
//
// A Windows mutex is owned by a thread, and is abandoned (free for the next
// process to take) when that thread ends. So it's taken on a goroutine
// locked to its own OS thread that never returns.
func acquireMainLock() bool {
	name, err := windows.UTF16PtrFromString(mainLockName())
	if err != nil {
		return false
	}
	got := make(chan bool)
	go func() {
		runtime.LockOSThread()
		h, _ := windows.CreateMutex(nil, false, name) // an existing one is opened
		if h == 0 {
			got <- false
			return
		}
		ev, err := windows.WaitForSingleObject(h, 0)
		// WAIT_ABANDONED: the last main instance ended (or crashed)
		// without releasing it. It's ours now.
		if err != nil || (ev != windows.WAIT_OBJECT_0 && ev != windows.WAIT_ABANDONED) {
			_ = windows.CloseHandle(h)
			got <- false
			return
		}
		got <- true
		select {} // keep the thread, and so the mutex, until the process ends
	}()
	return <-got
}

func lockFile(f *os.File) error {
	var ol windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &ol)
}

func unlockFile(f *os.File) {
	var ol windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
}
