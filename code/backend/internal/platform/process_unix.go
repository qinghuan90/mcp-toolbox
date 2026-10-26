//go:build darwin || linux

package platform

import (
	"os/exec"
	"syscall"
)

// Every native MCP server owns a process group. Package launchers such as npx
// and uvx may create descendants that outlive their direct child process.
func isolateProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func terminateProcessGroup(pid int) {
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}
