package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type healthSettings struct {
	Enabled  bool  `json:"enabled"`
	Interval int64 `json:"interval_seconds"`
}

func (a *App) healthSettings() healthSettings {
	return healthSettings{a.healthEnabled.Load(), a.healthInterval.Load()}
}
func (a *App) reloadHealthSettings(ctx context.Context) error {
	settings := healthSettings{true, 30}
	var value string
	err := a.DB.QueryRow(ctx, "SELECT value FROM system_settings WHERE key='health_check'").Scan(&value)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if err = json.Unmarshal([]byte(value), &settings); err != nil {
			return err
		}
	}
	if settings.Interval < 10 || settings.Interval > 3600 {
		return errors.New("健康检查间隔应为 10–3600 秒")
	}
	a.healthEnabled.Store(settings.Enabled)
	a.healthInterval.Store(settings.Interval)
	return nil
}
func (a *App) systemHealthCheck(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var settings healthSettings
	if !decode(w, r, &settings) {
		return
	}
	if settings.Interval < 10 || settings.Interval > 3600 {
		fail(w, 400, "健康检查间隔应为 10–3600 秒")
		return
	}
	value, _ := json.Marshal(settings)
	if _, err := a.DB.Exec(r.Context(), "INSERT INTO system_settings(key,value) VALUES('health_check',$1) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(value)); err != nil {
		fail(w, 503, "保存健康检查设置失败")
		return
	}
	a.healthEnabled.Store(settings.Enabled)
	a.healthInterval.Store(settings.Interval)
	respond(w, 200, settings)
}

// Start synchronously so Close always cancels and joins the worker before DB shutdown.
func (a *App) StartHealthChecks(parent context.Context) {
	a.healthOnce.Do(func() {
		ctx, cancel := context.WithCancel(parent)
		a.healthCancel = cancel
		a.healthWG.Add(1)
		go func() {
			defer a.healthWG.Done()
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			last := time.Now()
			for {
				select {
				case <-ctx.Done():
					return
				case now := <-ticker.C:
					if !a.healthEnabled.Load() {
						last = now
						continue
					}
					if max(time.Duration(0), now.Sub(last)) < time.Duration(a.healthInterval.Load())*time.Second {
						continue
					}
					a.checkHealth(ctx)
					last = time.Now()
				}
			}
		}()
	})
}
func (a *App) checkHealth(ctx context.Context) {
	if a.restoring.Load() || !a.maintenance.TryRLock() {
		return
	}
	defer a.maintenance.RUnlock()
	rows, err := a.DB.Query(ctx, "SELECT id FROM instances WHERE enabled AND status IN ('ready','fault')")
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	readErr := rows.Err()
	rows.Close()
	if readErr != nil {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, id := range ids {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(id string) { defer wg.Done(); defer func() { <-sem }(); a.checkInstanceHealth(ctx, id) }(id)
	}
	wg.Wait()
}
func (a *App) checkInstanceHealth(parent context.Context, id string) {
	lock := a.lock(id)
	if !lock.TryLock() {
		return
	}
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	var enabled bool
	var status string
	var uncertain bool
	if a.DB.QueryRow(ctx, `SELECT enabled,status,EXISTS(SELECT 1 FROM logs WHERE instance_id=instances.id AND runtime_generation=instances.runtime_generation AND status IN ('accepted','unknown')) FROM instances WHERE id=$1`, id).Scan(&enabled, &status, &uncertain) != nil || !enabled || uncertain || (status != "ready" && status != "fault") {
		return
	}
	i, err := a.loadInstance(ctx, id)
	if err != nil {
		return
	}
	if i.Transport == "stdio" && !a.AllowStdio.Load() {
		return
	}
	if value, ok := a.Runtime.Load(id); ok {
		s := value.(*snapshot)
		// Never probe or tear down a generation while a user call is executing.
		if !s.mu.TryLock() {
			return
		}
		if s.closed {
			s.mu.Unlock()
			return
		}
		probeCtx, stop := context.WithTimeout(ctx, 8*time.Second)
		err = probeInstance(probeCtx, s.session, s.instance)
		stop()
		s.mu.Unlock()
		if err == nil {
			s.healthUpdate(func(h *InstanceHealth) {
				h.connected(s.instance, len(s.tools))
				if isReferencePostgres(s.instance) {
					h.Business = stage("passed", "PostgreSQL 只读连接查询已通过；不代表全部业务已验证")
				}
			})
			return
		}
		if parent.Err() != nil {
			return
		}
		probeErr := err
		detail := "健康检查失败：" + redactStartupDiagnostic(s.instance, probeErr.Error())
		if _, err = a.DB.Exec(ctx, "UPDATE instances SET status='fault',last_error=$2 WHERE id=$1 AND enabled AND status='ready' AND runtime_generation=$3", id, detail, s.generation); err != nil {
			return
		}
		slog.Warn("MCP 健康检查失败", "event", "mcp_health_failed", "instance_id", id, "instance_name", i.Name, "detail", detail)
		s.healthUpdate(func(h *InstanceHealth) {
			var business *businessProbeError
			if errors.As(probeErr, &business) {
				h.Protocol = stage("passed", "MCP 健康探测已通过")
				h.Business = stage("failed", redactStartupDiagnostic(s.instance, probeErr.Error()))
			} else {
				h.Protocol = stage("failed", redactStartupDiagnostic(s.instance, probeErr.Error()))
				if h.Business.State == "passed" {
					h.Business = stage("unverified", "连接已失效，原业务验证不再代表当前可用")
				}
			}
		})
		s.healthMu.RLock()
		h := *s.health
		s.healthMu.RUnlock()
		a.stop(id)
		h.disconnected(s.instance)
		a.instanceHealth.Store(id, h)
		// Reconnect on the next interval; never replay a client's tool call.
		return
	}
	slog.Info("正在重试 MCP 连接", "event", "mcp_reconnecting", "instance_id", id, "instance_name", i.Name)
	if err = a.start(ctx, id); err != nil && parent.Err() == nil {
		writeCtx, stop := context.WithTimeout(parent, 3*time.Second)
		defer stop()
		a.DB.Exec(writeCtx, "UPDATE instances SET status='fault',last_error=$2 WHERE id=$1 AND enabled AND status!='quarantined'", id, a.startupError(writeCtx, id, err))
	} else if err == nil {
		slog.Info("MCP 连接已恢复", "event", "mcp_recovered", "instance_id", id, "instance_name", i.Name)
	}
}
func probeInstance(ctx context.Context, session *mcp.ClientSession, i Instance) error {
	if session == nil {
		return errors.New("MCP 连接已断开")
	}
	if err := session.Ping(ctx, nil); err != nil {
		var rpc *jsonrpc.Error
		if !errors.As(err, &rpc) || rpc.Code != -32601 {
			return err
		}
		if _, err = session.ListTools(ctx, nil); err != nil {
			return err
		}
	}
	if isReferencePostgres(i) {
		if err := verifyPostgres(ctx, session); err != nil {
			return &businessProbeError{err}
		}
	}
	return nil
}
