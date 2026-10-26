//go:build windows

package platform

import "golang.org/x/sys/windows"

func observeProcess(pid int) HealthStage {
	if pid <= 0 {
		return stage("unknown", "未获取本机进程标识")
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err == windows.ERROR_INVALID_PARAMETER {
		return stage("stopped", "本机进程已退出")
	}
	if err != nil {
		return stage("unknown", "无法确认本机进程是否存活")
	}
	defer windows.CloseHandle(handle)
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return stage("unknown", "无法读取本机进程状态")
	}
	if code == 259 { // STILL_ACTIVE
		return stage("running", "本机进程存在；进程存活不代表 MCP 可用")
	}
	return stage("stopped", "本机进程已退出")
}
