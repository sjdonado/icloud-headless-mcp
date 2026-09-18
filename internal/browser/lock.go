package browser

import (
	"os"
	"syscall"
	"time"
)

// AppLock serialises operations within one app, without blocking the
// other: two calls into the same app must not interleave clicks, while
// Notes and Reminders proceed independently. It waits rather than failing
// immediately, and refuses after the timeout instead of queueing forever.
func AppLock(stateDir, app string, timeout time.Duration) (func(), error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	fh, err := os.OpenFile(stateDir+"/"+app+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		if err := syscall.Flock(int(fh.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return func() {
				_ = syscall.Flock(int(fh.Fd()), syscall.LOCK_UN)
				_ = fh.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			_ = fh.Close()
			return nil, &LockError{App: app}
		}
		time.Sleep(time.Second)
	}
}

// LockError means another operation on the same app is still running.
type LockError struct{ App string }

func (e *LockError) Error() string { return "another " + e.App + " operation is still running" }
