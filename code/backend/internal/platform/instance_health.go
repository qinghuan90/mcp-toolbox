package platform

import "time"

// InstanceHealth describes observations of the current management connection.
// Protocol availability is deliberately distinct from business verification.
type InstanceHealth struct {
	Process    HealthStage `json:"process"`
	Protocol   HealthStage `json:"protocol"`
	Business   HealthStage `json:"business"`
	ToolCount  int         `json:"tool_count"`
	LastTool   string      `json:"last_tool,omitempty"`
	LastToolAt *time.Time  `json:"last_tool_at,omitempty"`
}
type HealthStage struct {
	State     string     `json:"state"`
	Detail    string     `json:"detail,omitempty"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}

func stage(state, detail string) HealthStage {
	now := time.Now().UTC()
	return HealthStage{state, detail, &now}
}
func newInstanceHealth(i Instance) *InstanceHealth {
	h := &InstanceHealth{Process: stage("unknown", "尚未观测本机进程"), Protocol: stage("unverified", "尚未完成 MCP 初始化与工具发现"), Business: stage("unverified", "未配置业务探针；MCP 连接成功不代表业务验证通过")}
	if i.Transport != "stdio" {
		h.Process = stage("not_applicable", "远程 HTTP 服务不由本机托管进程")
	}
	return h
}
func (h *InstanceHealth) connected(i Instance, count int) {
	if i.Transport == "stdio" {
		h.Process = stage("observed", "本次握手时已观测到本机进程；不保证持续存活")
	}
	h.Protocol = stage("passed", "MCP 初始化与工具发现已通过")
	h.ToolCount = count
}
func (h *InstanceHealth) disconnected(i Instance) {
	if i.Transport == "stdio" {
		h.Process = stage("stopped", "此连接的本机进程已关闭")
	}
	if h.Business.State == "passed" {
		h.Business = stage("unverified", "连接已关闭，原业务探针不再代表当前可用")
	}
	// Preserve evidence that handshake succeeded without claiming it is still live.
	if h.Protocol.State == "passed" {
		h.Protocol.State = "disconnected"
		h.Protocol.Detail = "本次 MCP 初始化与工具发现已通过；连接现已关闭"
	}
}
func (a *App) failedHealth(i Instance, h *InstanceHealth) {
	h.disconnected(i)
	// Failed replacement attempts must never overwrite a still-active generation.
	if _, active := a.Runtime.Load(i.ID); !active {
		a.instanceHealth.Store(i.ID, *h)
	}
}
func (a *App) observeHealth(i Instance) *InstanceHealth {
	if value, ok := a.Runtime.Load(i.ID); ok {
		s := value.(*snapshot)
		s.healthMu.RLock()
		var h *InstanceHealth
		if s.health != nil {
			copy := *s.health
			h = &copy
		}
		s.healthMu.RUnlock()
		if h == nil {
			h = newInstanceHealth(i)
		}
		if s.sessionEnded.Load() {
			h.disconnected(i)
		} else if i.Transport == "stdio" {
			h.Process = observeProcess(s.pid)
			if h.Process.State == "stopped" {
				h.disconnected(i)
			}
		}
		return h
	}
	if value, ok := a.instanceHealth.Load(i.ID); ok && i.Status == "fault" {
		h := value.(InstanceHealth)
		return &h
	}
	h := newInstanceHealth(i)
	if i.Transport == "stdio" {
		h.Process = stage("stopped", "未托管本机进程")
	}
	if i.Status == "quarantined" {
		h.Protocol = stage("disconnected", "执行结果不确定，已隔离，需人工确认")
	} else if i.Status == "fault" {
		h.Protocol = stage("unverified", "连接失败；暂无本次探测的分层证据")
	}
	return h
}
func (s *snapshot) healthUpdate(update func(*InstanceHealth)) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	if s.health == nil {
		s.health = newInstanceHealth(s.instance)
	}
	update(s.health)
}

type businessProbeError struct{ error }
