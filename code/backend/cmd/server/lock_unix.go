//go:build darwin || linux

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockDatabase holds an OS lock for the lifetime of the server. SQLite's own
// write locks do not protect the checkpoint-to-file-swap window during restore.
func lockDatabase(path string) (string, func(), error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return "", nil, err
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", nil, err
	}
	abs = filepath.Join(dir, filepath.Base(abs))
	lockPath := abs + ".lock"
	fd, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return "", nil, err
	}
	f := os.NewFile(uintptr(fd), lockPath)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return "", nil, errors.New("database lock path is not a regular file")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return "", nil, fmt.Errorf("database is already in use: %w", err)
	}
	return abs, func() {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
