//go:build windows

package autostart

import (
	"golang.org/x/sys/windows/registry"
)

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// Manager registers via the per-user HKCU Run key (no admin).
type Manager struct {
	exePath string
}

// New builds a windows autostart Manager for exePath.
func New(exePath string) (*Manager, error) {
	return &Manager{exePath: exePath}, nil
}

// Register sets the HKCU Run value to the daemon executable.
func (m *Manager) Register() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(appName, m.exePath)
}

// Unregister deletes the HKCU Run value.
func (m *Manager) Unregister() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	err = k.DeleteValue(appName)
	if err == registry.ErrNotExist {
		return nil
	}
	return err
}

// IsRegistered reports whether the HKCU Run value exists.
func (m *Manager) IsRegistered() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(appName)
	return err == nil
}
