package platform

import (
	"encoding/json"
	"io"
	"net/http"
)

func (a *App) serverConfiguration(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if a.Personal {
		fail(w, 403, "服务配置文件仅供企业版使用")
		return
	}
	if a.ServerConfigSnapshot == nil {
		fail(w, 503, "当前启动方式未配置服务配置文件")
		return
	}
	if r.Method == http.MethodGet {
		respond(w, 200, a.ServerConfigSnapshot())
		return
	}
	if a.SaveServerConfiguration == nil {
		fail(w, 403, "当前服务配置由外部部署管理")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil || !json.Valid(raw) {
		fail(w, 400, "配置必须为不超过64KiB的JSON对象")
		return
	}
	if err = a.SaveServerConfiguration(raw); err != nil {
		fail(w, 400, err.Error())
		return
	}
	respond(w, 200, a.ServerConfigSnapshot())
}
