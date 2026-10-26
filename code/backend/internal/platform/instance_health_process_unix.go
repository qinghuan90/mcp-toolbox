//go:build linux || darwin

package platform

import (
	"errors"
	"syscall"
)

// Signal 0 checks existence without interrupting the child process.
func observeProcess(pid int) HealthStage {
	if pid <= 0 {
		return stage("unknown", "未获取本机进程标识")
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return stage("running", "本机进程存在；进程存活不代表 MCP 或业务可用")
	}
	if errors.Is(err, syscall.ESRCH) {
		return stage("stopped", "本机进程已退出")
	}
	return stage("unknown", "无法确认本机进程是否存活")
}
