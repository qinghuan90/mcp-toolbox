package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type listenerSettings struct {
	Port int `json:"port"`
}

func pathForListenerSettings(databasePath string) string {
	return filepath.Join(filepath.Dir(databasePath), "listener-settings.json")
}

func readListenerPort(settingsPath string) (int, error) {
	info, err := os.Lstat(settingsPath)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return 0, errors.New("listener settings must be a private regular file")
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return 0, err
	}
	var settings listenerSettings
	if err = json.Unmarshal(data, &settings); err != nil {
		return 0, fmt.Errorf("invalid listener settings: %w", err)
	}
	if settings.Port < 1024 || settings.Port > 65535 {
		return 0, errors.New("invalid saved listener port")
	}
	return settings.Port, nil
}

func writeListenerPort(settingsPath string, port int) error {
	if port < 1024 || port > 65535 {
		return errors.New("invalid listener port")
	}
	if info, err := os.Lstat(settingsPath); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("listener settings must be a private regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(settingsPath)
	file, err := os.CreateTemp(dir, ".listener-settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err = file.Chmod(0600); err != nil {
		return err
	}
	if err = json.NewEncoder(file).Encode(listenerSettings{Port: port}); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), settingsPath); err != nil {
		return err
	}
	// The new file is already in place. A directory sync is best effort here:
	// reporting failure after rename would leave the live listener out of sync.
	if parent, openErr := os.Open(dir); openErr == nil {
		_ = parent.Sync()
		_ = parent.Close()
	}
	return nil
}
