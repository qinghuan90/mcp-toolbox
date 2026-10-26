//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func lockDatabase(path string) (string, func(), error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", nil, err
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return "", nil, err
	}
	directory, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", nil, err
	}
	abs = filepath.Join(directory, filepath.Base(abs))
	lockPath := abs + ".lock"
	if info, statErr := os.Lstat(lockPath); statErr == nil && !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("database lock path is not a regular file")
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", nil, err
	}
	if err = windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{}); err != nil {
		file.Close()
		return "", nil, fmt.Errorf("database is already in use: %w", err)
	}
	return abs, func() {
		_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &windows.Overlapped{})
		_ = file.Close()
	}, nil
}
