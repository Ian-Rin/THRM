//go:build !windows

package tray

import "time"

func isShellReady() bool {
	return true
}

// waitForShellReady 在非 Windows 平台无需等待外壳，直接返回。
func waitForShellReady(_ <-chan struct{}, _ time.Duration) bool {
	return true
}

// waitForTraySettle 在非 Windows 平台无需等待通知区域稳定，直接返回。
func waitForTraySettle(_ <-chan struct{}, _, _ time.Duration) {}
