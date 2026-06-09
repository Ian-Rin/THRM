//go:build windows

package temperature

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

func execCommandHiddenWithTimeout(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx := context.Background()
	cancel := func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	output, err := cmd.Output()
	if timeout > 0 && ctx.Err() != nil {
		return output, ctx.Err()
	}
	return output, err
}

