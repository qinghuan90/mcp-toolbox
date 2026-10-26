//go:build desktop

package main

type startupState struct {
	Supported bool   `json:"supported"`
	Enabled   bool   `json:"enabled"`
	Message   string `json:"message"`
}

func (f *desktopFiles) GetStartup() (startupState, error) {
	f.app.startupMu.Lock()
	defer f.app.startupMu.Unlock()
	return desktopStartupState(f.app.root)
}

func (f *desktopFiles) SetStartup(enabled bool) (startupState, error) {
	f.app.startupMu.Lock()
	defer f.app.startupMu.Unlock()
	return configureDesktopStartup(f.app.root, enabled)
}
