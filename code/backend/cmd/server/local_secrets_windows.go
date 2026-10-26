//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func secureWindowsSecret(path string) error {
	token, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	security, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := security.Owner()
	if err != nil || !owner.Equals(token.User.Sid) {
		return fmt.Errorf("本地密钥必须由当前 Windows 用户拥有")
	}
	// Ignore POSIX mode bits on Windows. Apply an explicit protected DACL for
	// the current user and LocalSystem, inherited by newly created contents.
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + token.User.Sid.String() + ")(A;OICI;FA;;;SY)")
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func privateOwnedPath(path string, _ os.FileInfo, _ os.FileMode) bool {
	return secureWindowsSecret(path) == nil
}

func lockSecretFile(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &windows.Overlapped{})
}

func unlockSecretFile(file *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &windows.Overlapped{})
}

func privateSecretTemp(file *os.File) error { return secureWindowsSecret(file.Name()) }
