//go:build !windows

package main

import (
	"os"
	"syscall"
)

func privateOwnedPath(_ string, info os.FileInfo, mode os.FileMode) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid()) && info.Mode().Perm() == mode
}

func lockSecretFile(file *os.File) error    { return syscall.Flock(int(file.Fd()), syscall.LOCK_EX) }
func unlockSecretFile(file *os.File)        { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }
func privateSecretTemp(file *os.File) error { return file.Chmod(0600) }
