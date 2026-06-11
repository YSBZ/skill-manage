// Package autostart registers the daemon to start at login, using the
// least-privilege mechanism per platform (R19): a per-user LaunchAgent on
// macOS, the HKCU Run key on Windows, and a guarded ~/.profile hook on
// Linux/WSL. The daemon's own scheduling is platform-uniform; only this
// one-time registration is platform-specific.
//
// Each platform file (build-tagged) provides:
//
//	type Manager
//	func New(exePath string) (*Manager, error)
//	func (*Manager) Register() error
//	func (*Manager) Unregister() error
//	func (*Manager) IsRegistered() bool
package autostart

const (
	// label is the LaunchAgent label / reverse-DNS identifier (macOS).
	label = "com.mcstack.skillmanage"
	// appName is the registry value name (Windows) and profile marker tag.
	appName = "SkillManage"
)
