package update

import (
	"syscall"
	"testing"
)

func TestMapShellExecuteLastError(t *testing.T) {
	t.Run("UAC cancellation maps to sentinel", func(t *testing.T) {
		got := mapShellExecuteLastError(syscall.Errno(shellExecuteErrorCancelledCode))
		if got != ErrUpdateCancelled {
			t.Fatalf("error = %v, want ErrUpdateCancelled", got)
		}
	})

	t.Run("ordinary Win32 error is preserved", func(t *testing.T) {
		want := syscall.Errno(5) // ERROR_ACCESS_DENIED
		got := mapShellExecuteLastError(want)
		if got != want {
			t.Fatalf("error = %v (%T), want original %v (%T)", got, got, want, want)
		}
	})

	t.Run("missing last error fails closed", func(t *testing.T) {
		if got := mapShellExecuteLastError(nil); got == nil {
			t.Fatal("error = nil, want a failure when ShellExecuteExW returned false")
		}
	})
}
