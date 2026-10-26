package platform

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"
	"toolbox/internal/runlog"
)

func (a *App) systemLogs(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if a.RunLogs == nil {
		fail(w, 503, "系统日志尚未初始化")
		return
	}
	q := r.URL.Query()
	f := runlog.Filter{Category: q.Get("category"), Level: q.Get("level"), Search: q.Get("q"), Cursor: q.Get("cursor"), Limit: 50}
	if f.Category == "" {
		f.Category = "exception"
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			fail(w, 400, "每页数量须为 1–200")
			return
		}
		f.Limit = n
	}
	for key, target := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if v := q.Get(key); v != "" {
			parsed, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				fail(w, 400, "时间须为 RFC3339 格式")
				return
			}
			*target = parsed
		}
	}
	page, err := a.RunLogs.Query(r.Context(), f)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		slog.Warn("系统日志读取失败", "detail", err.Error())
		fail(w, 400, "无法读取日志："+err.Error())
		return
	}
	respond(w, 200, page)
}
