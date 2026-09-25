package update

import (
	"errors"
	"syscall"
)

const shellExecuteErrorCancelledCode = 1223

func mapShellExecuteLastError(lastErr error) error {
	if lastErr == nil || errors.Is(lastErr, syscall.Errno(0)) {
		return errors.New("ShellExecuteExW failed without a last-error code")
	}
	if errors.Is(lastErr, syscall.Errno(shellExecuteErrorCancelledCode)) {
		return ErrUpdateCancelled
	}
	return lastErr
}
