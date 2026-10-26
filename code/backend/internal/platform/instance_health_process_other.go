//go:build !linux && !darwin && !windows

package platform

func observeProcess(pid int) HealthStage {
	return stage("unknown", "此系统尚不支持独立进程存活检查；请查看 MCP 连接状态")
}
