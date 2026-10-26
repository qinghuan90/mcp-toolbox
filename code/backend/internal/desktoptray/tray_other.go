//go:build desktop && !darwin

package desktoptray

// Other desktop packages retain the application menu and background service.
// Native tray integration is enabled only for the macOS package tested here.
func Start(func(int)) {}
func Stop()           {}

func Supported() bool   { return false }
func RequestQuit() bool { return false }
