//go:build desktop && windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func init() {
	version := windows.RtlGetVersion()
	if version.MajorVersion < 10 || version.BuildNumber < 22000 || version.ProductType != 1 {
		message, _ := syscall.UTF16PtrFromString("Toolbox 桌面版需要 Windows 11 或更高版本（内部版本 22000+）。")
		title, _ := syscall.UTF16PtrFromString("无法启动 Toolbox")
		windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(message)), uintptr(unsafe.Pointer(title)), 0x10)
		os.Exit(1)
	}
	// GUI launches do not source shell profiles. Include standard per-user
	// runtime locations; never execute a shell startup script.
	paths := []string{os.Getenv("PATH")}
	for _, item := range []string{
		filepath.Join(os.Getenv("APPDATA"), "npm"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Python", "Launcher"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WindowsApps"),
		filepath.Join(os.Getenv("ProgramFiles"), "nodejs"),
		filepath.Join(os.Getenv("USERPROFILE"), ".local", "bin"),
		filepath.Join(os.Getenv("USERPROFILE"), ".volta", "bin"),
	} {
		if info, err := os.Stat(item); err == nil && info.IsDir() {
			paths = append(paths, item)
		}
	}
	_ = os.Setenv("PATH", strings.Join(paths, string(os.PathListSeparator)))
}
