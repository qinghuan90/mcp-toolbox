package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Missing secrets are generated from OS entropy and persisted for this host.
// Existing encrypted databases never receive a replacement MASTER_KEY.
func ensureLocalSecrets(databasePath string) error {
	master, setup := os.Getenv("MASTER_KEY"), os.Getenv("SETUP_KEY")
	if master != "" && setup != "" {
		return nil
	}

	configRoot := os.Getenv("XDG_CONFIG_HOME")
	if configRoot == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		configRoot = filepath.Join(home, ".config")
	}
	secretPath := os.Getenv("TOOLBOX_SECRETS_FILE")
	if secretPath == "" {
		secretPath = filepath.Join(configRoot, "toolbox", "local.env")
	}
	secretPath, err := filepath.Abs(secretPath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(secretPath)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || !privateOwnedPath(dir, info, 0700) {
		return errors.New("local secrets directory must be owned by this user with mode 0700")
	}
	lock, err := os.OpenFile(secretPath+".lock", os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	info, err = os.Lstat(secretPath + ".lock")
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !privateOwnedPath(secretPath+".lock", info, 0600) {
		return errors.New("local secrets lock must be a private regular file")
	}
	if err = lockSecretFile(lock); err != nil {
		return err
	}
	defer unlockSecretFile(lock)

	storedMaster, storedSetup, err := readLocalSecrets(secretPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	changed := false
	if master == "" {
		master = storedMaster
		if master == "" {
			if _, dbErr := os.Lstat(databasePath); dbErr == nil {
				return errors.New("existing SQLite database requires its original MASTER_KEY; refusing to create a new one")
			} else if !errors.Is(dbErr, os.ErrNotExist) {
				return dbErr
			}
			bytes := make([]byte, 32)
			if _, err = rand.Read(bytes); err != nil {
				return err
			}
			master = base64.StdEncoding.EncodeToString(bytes)
			storedMaster = master
			changed = true
		}
	}
	if setup == "" {
		setup = storedSetup
		if setup == "" {
			bytes := make([]byte, 24)
			if _, err = rand.Read(bytes); err != nil {
				return err
			}
			setup = hex.EncodeToString(bytes)
			storedSetup = setup
			changed = true
		}
	}
	if changed {
		if err = writeLocalSecrets(secretPath, storedMaster, storedSetup); err != nil {
			return err
		}
		slog.Info("local secrets generated", "path", secretPath)
	}
	if err = os.Setenv("MASTER_KEY", master); err != nil {
		return err
	}
	return os.Setenv("SETUP_KEY", setup)
}

func readLocalSecrets(secretPath string) (string, string, error) {
	info, err := os.Lstat(secretPath)
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() || !privateOwnedPath(secretPath, info, 0600) {
		return "", "", errors.New("local secrets file must be a private regular file owned by this user with mode 0600")
	}
	data, err := os.ReadFile(secretPath)
	if err != nil {
		return "", "", err
	}
	if len(data) == 0 || len(data) > 4096 {
		return "", "", errors.New("invalid local secrets file size")
	}
	var master, setup string
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "MASTER_KEY=") && master == "":
			master = strings.TrimPrefix(line, "MASTER_KEY=")
		case strings.HasPrefix(line, "SETUP_KEY=") && setup == "":
			setup = strings.TrimPrefix(line, "SETUP_KEY=")
		default:
			return "", "", errors.New("invalid local secrets file")
		}
	}
	if master == "" && setup == "" {
		return "", "", errors.New("invalid local secrets file")
	}
	if master != "" {
		key, decodeErr := base64.StdEncoding.DecodeString(master)
		if decodeErr != nil || len(key) != 32 {
			return "", "", errors.New("invalid local MASTER_KEY")
		}
	}
	if setup != "" && len(setup) < 16 {
		return "", "", errors.New("invalid local SETUP_KEY")
	}
	return master, setup, nil
}

func writeLocalSecrets(secretPath, master, setup string) error {
	file, err := os.CreateTemp(filepath.Dir(secretPath), ".local.env-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err = privateSecretTemp(file); err != nil {
		return err
	}
	value := ""
	if master != "" {
		value += fmt.Sprintf("MASTER_KEY=%s\n", master)
	}
	if setup != "" {
		value += fmt.Sprintf("SETUP_KEY=%s\n", setup)
	}
	if _, err = file.WriteString(value); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), secretPath); err != nil {
		return err
	}
	if parent, openErr := os.Open(filepath.Dir(secretPath)); openErr == nil {
		_ = parent.Sync()
		_ = parent.Close()
	}
	return nil
}
