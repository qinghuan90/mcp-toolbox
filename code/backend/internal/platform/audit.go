package platform

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

type outcome struct {
	ID       string
	Status   string
	Detail   string
	Duration int64
}

func journalDir() string {
	if p := os.Getenv("AUDIT_DIR"); p != "" {
		return p
	}
	return "./data/audit"
}

// Only metadata is journaled; arguments, credentials and results are never persisted here.
func (a *App) auditOutcome(id, status, detail string, duration int64) {
	a.auditMu.Lock()
	defer a.auditMu.Unlock()
	a.auditBlocked.Store(true)
	persisted := false
	defer func() {
		if !persisted {
			a.auditLost.Store(true)
		}
	}()
	dir := journalDir()
	if e := os.MkdirAll(dir, 0700); e != nil {
		slog.Error("audit journal unavailable")
		return
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) >= 10000 {
		slog.Error("audit journal full or unavailable")
		return
	}
	b, _ := json.Marshal(outcome{id, status, detail, duration})
	f, e := os.OpenFile(filepath.Join(dir, id+".tmp"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return
	}
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	f.Close()
	if e == nil {
		e = os.Rename(filepath.Join(dir, id+".tmp"), filepath.Join(dir, id+".json"))
	}
	if e != nil {
		slog.Error("audit journal write failed")
		return
	}
	d, e := os.Open(dir)
	if e != nil {
		return
	}
	e = d.Sync()
	d.Close()
	if e != nil {
		return
	}
	persisted = true
}
func (a *App) replayAudit(ctx context.Context) {
	a.auditMu.Lock()
	defer a.auditMu.Unlock()
	files, e := os.ReadDir(journalDir())
	if os.IsNotExist(e) {
		if !a.auditLost.Load() {
			a.auditBlocked.Store(false)
		}
		return
	}
	if e != nil {
		a.auditBlocked.Store(true)
		return
	}
	for _, f := range files {
		if filepath.Ext(f.Name()) != ".json" {
			a.auditBlocked.Store(true)
			a.auditLost.Store(true)
			return
		}
		p := filepath.Join(journalDir(), f.Name())
		b, e := os.ReadFile(p)
		if e != nil {
			a.auditBlocked.Store(true)
			return
		}
		var out outcome
		if json.Unmarshal(b, &out) != nil {
			a.auditBlocked.Store(true)
			return
		}
		_, e = a.DB.Exec(ctx, "UPDATE logs SET status=$2,detail=$3,duration_ms=$4 WHERE id=$1 AND status IN ('accepted','unknown')", out.ID, out.Status, out.Detail, out.Duration)
		if e != nil {
			a.auditBlocked.Store(true)
			return
		}
		if os.Remove(p) != nil {
			a.auditBlocked.Store(true)
			return
		}
	}
	if !a.auditLost.Load() {
		a.auditBlocked.Store(false)
	}
}
func (a *App) RecoverAudit(ctx context.Context) { a.replayAudit(ctx) }
func (a *App) RunAuditRecovery(ctx context.Context) {
	a.maintenance.RLock()
	a.replayAudit(ctx)
	a.maintenance.RUnlock()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.maintenance.RLock()
			a.replayAudit(ctx)
			a.maintenance.RUnlock()
		}
	}
}
