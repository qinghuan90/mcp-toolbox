//go:build desktop && darwin

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func startupAgent(root string) (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(root) {
		return "", "", errors.New("无法确定本机启动目录")
	}
	digest := sha256.Sum256([]byte(root))
	label := "com.toolbox.desktop." + hex.EncodeToString(digest[:8])
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), label, nil
}

func desktopStartupState(root string) (startupState, error) {
	state := startupState{Supported: true, Message: "登录 macOS 后自动启动；关闭窗口时继续在后台运行。"}
	path, _, err := startupAgent(root)
	if err != nil {
		return state, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if !info.Mode().IsRegular() {
		return state, errors.New("启动项不是普通配置文件，请检查系统登录项")
	}
	state.Enabled = true
	return state, nil
}

func xmlStartupValue(value string) string {
	var out bytes.Buffer
	_ = xml.EscapeText(&out, []byte(value))
	return out.String()
}

func configureDesktopStartup(root string, enabled bool) (startupState, error) {
	path, label, err := startupAgent(root)
	if err != nil {
		return startupState{}, err
	}
	if !enabled {
		if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
			return startupState{}, err
		}
		return desktopStartupState(root)
	}
	executable, err := os.Executable()
	if err != nil {
		return startupState{}, err
	}
	index := strings.LastIndex(executable, ".app/Contents/MacOS/")
	if index < 0 {
		return startupState{}, errors.New("请使用完整 Toolbox.app 设置开机启动")
	}
	source := executable[:index+4]
	binary, err := os.Open(executable)
	if err != nil {
		return startupState{}, err
	}
	digest := sha256.New()
	_, err = io.Copy(digest, binary)
	binary.Close()
	if err != nil {
		return startupState{}, err
	}
	// Keep a complete, versioned application in a stable private location. A
	// downloaded application's App Translocation path is invalid after reboot.
	base := filepath.Join(root, "autostart")
	if err = os.MkdirAll(base, 0700); err != nil {
		return startupState{}, err
	}
	info, err := os.Lstat(base)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return startupState{}, errors.New("自动启动目录不可用")
	}
	versionDir := filepath.Join(base, hex.EncodeToString(digest.Sum(nil))[:20])
	installed := filepath.Join(versionDir, "Toolbox.app")
	if _, err = os.Stat(filepath.Join(installed, "Contents", "MacOS", "Toolbox")); os.IsNotExist(err) {
		staging, err := os.MkdirTemp(base, ".install-")
		if err != nil {
			return startupState{}, err
		}
		defer os.RemoveAll(staging)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err = exec.CommandContext(ctx, "/usr/bin/ditto", source, filepath.Join(staging, "Toolbox.app")).Run(); err != nil {
			return startupState{}, errors.New("保存开机启动应用失败，请检查磁盘空间")
		}
		if err = os.Rename(staging, versionDir); err != nil {
			return startupState{}, err
		}
	} else if err != nil {
		return startupState{}, err
	}
	content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>--data-dir</string><string>%s</string></array>
<key>RunAtLoad</key><true/><key>LimitLoadToSessionType</key><string>Aqua</string>
<key>ProcessType</key><string>Interactive</string>
</dict></plist>
`, xmlStartupValue(label), xmlStartupValue(filepath.Join(installed, "Contents", "MacOS", "Toolbox")), xmlStartupValue(root))
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return startupState{}, err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".toolbox-startup-*")
	if err != nil {
		return startupState{}, err
	}
	defer os.Remove(file.Name())
	if _, err = file.WriteString(content); err != nil {
		file.Close()
		return startupState{}, err
	}
	if err = file.Close(); err != nil {
		return startupState{}, err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return startupState{}, err
	}
	return desktopStartupState(root)
}
