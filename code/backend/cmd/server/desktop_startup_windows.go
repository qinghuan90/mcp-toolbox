//go:build desktop && windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

const startupRegistryPath = `Software\Microsoft\Windows\CurrentVersion\Run`

func startupRegistryName(root string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return "Toolbox-" + hex.EncodeToString(sum[:8])
}

func desktopStartupState(root string) (startupState, error) {
	state := startupState{Supported: true, Message: "登录 Windows 后自动启动当前 Toolbox；请保留程序所在目录"}
	key, err := registry.OpenKey(registry.CURRENT_USER, startupRegistryPath, registry.QUERY_VALUE)
	if err == registry.ErrNotExist {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	defer key.Close()
	value, _, err := key.GetStringValue(startupRegistryName(root))
	if err == registry.ErrNotExist {
		return state, nil
	}
	state.Enabled = err == nil && value != ""
	return state, err
}

func configureDesktopStartup(root string, enabled bool) (startupState, error) {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, startupRegistryPath, registry.SET_VALUE)
	if err != nil {
		return startupState{}, fmt.Errorf("无法修改当前用户开机启动: %w", err)
	}
	defer key.Close()
	name := startupRegistryName(root)
	if enabled {
		executable, err := os.Executable()
		if err != nil {
			return startupState{}, err
		}
		// Windows paths cannot contain a double quote. Quote both paths without
		// involving cmd.exe; spaces and non-ASCII user names remain intact.
		command := `"` + executable + `" --data-dir "` + root + `"`
		if len(command) > 260 {
			return startupState{}, fmt.Errorf("开机启动命令超过 Windows Run 键的 260 字符限制，请将程序移到较短路径")
		}
		err = key.SetStringValue(name, command)
	} else {
		err = key.DeleteValue(name)
		if err == registry.ErrNotExist {
			err = nil
		}
	}
	if err != nil {
		return startupState{}, err
	}
	return desktopStartupState(root)
}
