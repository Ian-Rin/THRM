//go:build !windows

package hotkey

import (
	"fmt"
	"strings"
	"sync"

	"github.com/TIANLI0/THRM/internal/types"
	hotkeylib "golang.design/x/hotkey"
)

type Action string

const (
	ActionToggleManualGear   Action = "toggle-manual-gear"
	ActionToggleAutoMode     Action = "toggle-auto-control"
	ActionToggleCurveProfile Action = "toggle-curve-profile"
)

type Manager struct {
	logger   types.Logger
	onAction func(action Action, shortcut string)

	mutex  sync.Mutex
	closed bool
}

func NewManager(logger types.Logger, onAction func(action Action, shortcut string)) *Manager {
	return &Manager{
		logger:   logger,
		onAction: onAction,
	}
}

func (m *Manager) UpdateBindings(manualGearShortcut, autoControlShortcut, curveProfileShortcut string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.closed {
		return fmt.Errorf("hotkey manager already stopped")
	}

	var errs []string
	if manualGearShortcut != "" {
		if _, _, err := ParseShortcut(manualGearShortcut); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if autoControlShortcut != "" {
		if _, _, err := ParseShortcut(autoControlShortcut); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if curveProfileShortcut != "" {
		if _, _, err := ParseShortcut(curveProfileShortcut); err != nil {
			errs = append(errs, err.Error())
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func (m *Manager) Stop() {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.closed = true
}

func ParseShortcut(input string) ([]hotkeylib.Modifier, hotkeylib.Key, error) {
	normalized := normalizeShortcut(input)
	if normalized == "" {
		return nil, hotkeylib.Key(0), fmt.Errorf("empty shortcut")
	}

	parts := strings.Split(normalized, "+")
	if len(parts) < 2 {
		return nil, hotkeylib.Key(0), fmt.Errorf("missing modifier")
	}

	last := strings.TrimSpace(parts[len(parts)-1])
	if last == "" {
		return nil, hotkeylib.Key(0), fmt.Errorf("missing main key")
	}

	return nil, hotkeylib.Key(0), nil
}

func normalizeShortcut(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}

	input = strings.ReplaceAll(input, " ", "")
	input = strings.ReplaceAll(input, "-", "+")
	input = strings.ReplaceAll(input, "_", "+")
	input = strings.ReplaceAll(input, "＋", "+")
	input = strings.ReplaceAll(input, "，", "+")
	input = strings.ReplaceAll(input, ",", "+")

	parts := strings.Split(input, "+")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, strings.ToUpper(p))
	}
	return strings.Join(out, "+")
}

