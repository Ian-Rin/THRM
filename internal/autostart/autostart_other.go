//go:build !windows

package autostart

import (
	"github.com/TIANLI0/THRM/internal/types"
)

type Manager struct {
	logger types.Logger
}

func NewManager(logger types.Logger) *Manager {
	return &Manager{logger: logger}
}

func (m *Manager) IsRunningAsAdmin() bool {
	return false
}

func (m *Manager) SetWindowsAutoStart(enable bool) error {
	return nil
}

func (m *Manager) GetAutoStartMethod() string {
	return "none"
}

func (m *Manager) SetAutoStartWithMethod(enable bool, method string) error {
	return nil
}

func (m *Manager) CheckWindowsAutoStart() bool {
	return false
}

func DetectAutoStartLaunch(args []string) bool {
	for _, arg := range args {
		if arg == "--autostart" || arg == "/autostart" || arg == "-autostart" {
			return true
		}
	}
	return false
}

