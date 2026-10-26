package platform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"toolbox/internal/storage"
)

const maxRestoreSize = 512 << 20

type stagedRestore struct {
	Path    string
	Created time.Time
}

func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if current(r).Role != "admin" {
		fail(w, 403, "需要管理员权限")
		return false
	}
	return true
}

func environmentCheck(name string) map[string]any {
	command, err := runtimeCommand(name)
	if err != nil {
		return map[string]any{"available": false}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	process := exec.CommandContext(ctx, command, "--version")
	process.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	if err := preparePlatformCommand(process); err != nil {
		return map[string]any{"available": true, "version": "未知"}
	}
	output, err := process.CombinedOutput()
	if err != nil {
		return map[string]any{"available": true, "version": "未知"}
	}
	version := strings.TrimSpace(string(output))
	if len(version) > 100 {
		version = version[:100]
	}
	return map[string]any{"available": true, "version": version}
}

func (a *App) systemStatus(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var journal string
	if err := a.DB.QueryRow(r.Context(), "PRAGMA journal_mode").Scan(&journal); err != nil {
		fail(w, 503, "数据库不可用")
		return
	}
	rows, err := a.DB.Query(r.Context(), "SELECT status,count(*) FROM instances GROUP BY status")
	if err != nil {
		fail(w, 503, "查询失败")
		return
	}
	counts := map[string]int{"ready": 0, "stopped": 0, "fault": 0, "quarantined": 0, "starting": 0}
	for rows.Next() {
		var status string
		var count int
		if rows.Scan(&status, &count) == nil {
			counts[status] = count
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		fail(w, 503, "查询失败")
		return
	}
	rows.Close()
	version, err := storage.Version(r.Context(), a.DB)
	if err != nil {
		fail(w, 503, "数据库不可用")
		return
	}
	env := map[string]any{"node": environmentCheck("node"), "npx": environmentCheck("npx"), "python": environmentCheck("python3"), "uv": environmentCheck("uv"), "uvx": environmentCheck("uvx"), "native_stdio": a.AllowStdio.Load(), "native_stdio_editable": !a.StdioForced}
	port := 0
	if a.ListenerPort != nil {
		port = a.ListenerPort()
	}
	respond(w, 200, map[string]any{"database": "sqlite", "database_file": filepath.Base(a.DB.Path), "schema_version": version, "wal": journal == "wal", "runtime": counts, "environment": env, "health_check": a.healthSettings(), "listen_port": port, "port_editable": a.PortEditable})
}

func (a *App) reloadStdioSetting(ctx context.Context) error {
	var value string
	err := a.DB.QueryRow(ctx, "SELECT value FROM system_settings WHERE key='native_stdio'").Scan(&value)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// A missing setting means the edition default; an explicit user choice is
	// retained across upgrades and restore, including an explicit disable.
	if errors.Is(err, sql.ErrNoRows) {
		value = "true"
	}
	a.AllowStdio.Store(a.StdioForced || value == "true")
	return nil
}

func (a *App) systemNativeStdio(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if a.StdioForced {
		fail(w, 409, "当前部署的本机命令权限由启动配置管理")
		return
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	a.stdioMu.Lock()
	defer a.stdioMu.Unlock()
	if !in.Enabled {
		var active int
		if err := a.DB.QueryRow(r.Context(), "SELECT count(*) FROM instances WHERE transport='stdio' AND enabled=1").Scan(&active); err != nil {
			fail(w, 503, "无法检查正在运行的 stdio 实例")
			return
		}
		if active != 0 {
			fail(w, 409, "请先在实例页面停止所有 stdio 实例，再关闭本机运行")
			return
		}
		inUse := false
		a.Runtime.Range(func(_, value any) bool {
			if value.(*snapshot).instance.Transport == "stdio" {
				inUse = true
				return false
			}
			return true
		})
		if inUse {
			fail(w, 409, "仍有 stdio 连接在使用，请先停止实例")
			return
		}
	}
	value := "false"
	if in.Enabled {
		value = "true"
	}
	if _, err := a.DB.Exec(r.Context(), "INSERT INTO system_settings(key,value) VALUES('native_stdio',$1) ON CONFLICT(key) DO UPDATE SET value=excluded.value", value); err != nil {
		fail(w, 503, "保存本机运行设置失败")
		return
	}
	a.AllowStdio.Store(in.Enabled)
	respond(w, 200, map[string]bool{"enabled": in.Enabled})
}

func (a *App) systemListener(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if !a.PortEditable || a.ChangePort == nil {
		fail(w, 409, "当前部署的监听地址由启动参数或外部配置管理")
		return
	}
	var in struct {
		Port int `json:"port"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Port < 1024 || in.Port > 65535 {
		fail(w, 400, "端口须在 1024–65535 之间")
		return
	}
	address, err := a.ChangePort(in.Port)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	respond(w, 200, map[string]any{"url": address, "port": in.Port})
}

func (a *App) systemBackup(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	file, err := os.CreateTemp(filepath.Dir(a.DB.Path), ".toolbox-backup-*.sqlite")
	if err != nil {
		fail(w, 503, "无法创建备份")
		return
	}
	path := file.Name()
	file.Close()
	os.Remove(path)
	defer os.Remove(path)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err = a.DB.Backup(ctx, path); err != nil {
		fail(w, 503, "备份失败")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		fail(w, 503, "备份读取失败")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		fail(w, 503, "备份文件无效")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="toolbox-backup.sqlite"`)
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	w.WriteHeader(200)
	io.Copy(w, f)
}

func (a *App) validateRestoreFile(ctx context.Context, path string) (map[string]int64, error) {
	db, err := storage.Open(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var admins int
	if err = db.QueryRow(ctx, "SELECT count(*) FROM users WHERE role='admin' AND enabled").Scan(&admins); err != nil || admins < 1 {
		return nil, errors.New("backup has no enabled administrator")
	}
	rows, err := db.Query(ctx, "SELECT secret FROM instances UNION ALL SELECT secret FROM config_versions UNION ALL SELECT secret FROM discoveries WHERE secret IS NOT NULL")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var blob []byte
		if err = rows.Scan(&blob); err != nil {
			rows.Close()
			return nil, err
		}
		if _, err = a.unseal(blob); err != nil {
			rows.Close()
			return nil, errors.New("MASTER_KEY cannot decrypt backup credentials")
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if err = a.verifyMCPOAuthSecrets(ctx, db); err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, table := range []string{"users", "instances", "tokens", "rules", "logs", "discoveries"} {
		n, countErr := db.Count(ctx, table)
		if countErr != nil {
			return nil, countErr
		}
		counts[table] = n
	}
	return counts, nil
}

func (a *App) systemRestoreUpload(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRestoreSize+(8<<20))
	fh, _, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "需要 SQLite 备份文件")
		return
	}
	defer fh.Close()
	file, err := os.CreateTemp(filepath.Dir(a.DB.Path), ".toolbox-restore-*.sqlite")
	if err != nil {
		fail(w, 503, "无法暂存恢复文件")
		return
	}
	path := file.Name()
	keep := false
	defer func() {
		if !keep {
			os.Remove(path)
			os.Remove(path + "-wal")
			os.Remove(path + "-shm")
		}
	}()
	n, err := io.CopyN(file, fh, maxRestoreSize+1)
	if err != nil && err != io.EOF {
		file.Close()
		fail(w, 400, "文件读取失败")
		return
	}
	if n == 0 || n > maxRestoreSize {
		file.Close()
		fail(w, 400, "备份文件大小无效")
		return
	}
	if err = file.Sync(); err != nil {
		file.Close()
		fail(w, 503, "暂存失败")
		return
	}
	file.Close()
	counts, err := a.validateRestoreFile(r.Context(), path)
	if err != nil {
		fail(w, 400, "备份校验失败：结构、完整性或密钥不匹配")
		return
	}
	id := ID()
	a.restoreMu.Lock()
	for key, entry := range a.restoreFiles {
		if time.Since(entry.Created) > 30*time.Minute {
			os.Remove(entry.Path)
			delete(a.restoreFiles, key)
		}
	}
	a.restoreFiles[id] = stagedRestore{Path: path, Created: time.Now()}
	a.restoreMu.Unlock()
	time.AfterFunc(30*time.Minute, func() {
		a.restoreMu.Lock()
		entry, ok := a.restoreFiles[id]
		if ok {
			delete(a.restoreFiles, id)
		}
		a.restoreMu.Unlock()
		if ok {
			os.Remove(entry.Path)
			removeSQLiteSidecars(entry.Path)
		}
	})
	keep = true
	respond(w, 200, map[string]any{"restore_id": id, "counts": counts, "expires_in_seconds": 1800, "message": "备份校验通过。确认后将进入维护态、保存当前数据库并执行恢复。"})
}

func (a *App) systemRestoreCommit(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var input struct {
		Confirm string `json:"confirm"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Confirm != "RESTORE" {
		fail(w, 400, "确认字段应为 RESTORE")
		return
	}
	a.restoreMu.Lock()
	entry, ok := a.restoreFiles[r.PathValue("id")]
	a.restoreMu.Unlock()
	if !ok || time.Since(entry.Created) > 30*time.Minute {
		fail(w, 404, "恢复文件不存在或已过期")
		return
	}
	if !a.restoring.CompareAndSwap(false, true) {
		fail(w, 409, "已有恢复操作进行中")
		return
	}
	defer func() {
		checkCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if a.DB != nil && a.DB.Ping(checkCtx) == nil {
			a.restoring.Store(false)
		}
	}()
	if !a.maintenance.TryLock() {
		fail(w, 409, "仍有请求或任务执行中，请稍后重试")
		return
	}
	defer a.maintenance.Unlock()
	var active int
	if err := a.DB.QueryRow(r.Context(), "SELECT count(*) FROM tasks WHERE status IN ('queued','running')").Scan(&active); err != nil || active > 0 {
		fail(w, 409, "仍有任务执行中，请稍后重试")
		return
	}
	if a.auditBlocked.Load() {
		fail(w, 409, "审计恢复未完成，暂不能恢复数据库")
		return
	}
	// Claim the file before revalidation. The expiration timer only removes
	// entries still in the map, so it cannot unlink a file during cutover.
	a.restoreMu.Lock()
	entry, ok = a.restoreFiles[r.PathValue("id")]
	if ok {
		delete(a.restoreFiles, r.PathValue("id"))
	}
	a.restoreMu.Unlock()
	if !ok || time.Since(entry.Created) > 30*time.Minute {
		fail(w, 404, "恢复文件不存在或已过期")
		return
	}
	defer os.Remove(entry.Path)
	if _, err := a.validateRestoreFile(r.Context(), entry.Path); err != nil {
		fail(w, 400, "恢复文件重新校验失败")
		return
	}
	if err := a.applyRestore(r.Context(), entry.Path, r.PathValue("id")); err != nil {
		fail(w, 503, "恢复失败，已尝试保留原数据库")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "toolbox_session", Value: "", Path: "/", HttpOnly: true, Secure: a.Secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	respond(w, 200, map[string]any{"ok": true, "message": "数据库已恢复并重新加载服务。原数据库保存在受控数据目录。"})
}

func (a *App) applyRestore(_ context.Context, candidate, id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	path := a.DB.Path
	backup := path + ".restore.rollback-" + id
	marker := path + ".restore.pending"
	if err := os.WriteFile(marker, []byte(id), 0600); err != nil {
		return err
	}
	if err := syncFileAndDir(marker); err != nil {
		os.Remove(marker)
		return err
	}
	var busy, logPages, checkpointed int
	if err := a.DB.QueryRow(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logPages, &checkpointed); err != nil {
		os.Remove(marker)
		return err
	}
	if busy != 0 || (logPages >= 0 && checkpointed < logPages) {
		os.Remove(marker)
		return errors.New("SQLite WAL checkpoint busy or incomplete")
	}
	a.closeRuntime()
	if err := a.DB.Close(); err != nil {
		return a.resumeOriginal(path, marker, err)
	}
	if err := removeSQLiteSidecars(path); err != nil {
		return a.resumeOriginal(path, marker, err)
	}
	if err := os.Rename(path, backup); err != nil {
		return a.resumeOriginal(path, marker, err)
	}
	if err := os.Rename(candidate, path); err != nil {
		return a.rollbackRestore(path, backup, marker, err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return a.rollbackRestore(path, backup, marker, err)
	}
	newdb, err := storage.Open(path)
	if err != nil {
		return a.rollbackRestore(path, backup, marker, err)
	}
	a.DB = newdb
	if _, err = a.validateRestoredCurrent(ctx); err != nil {
		newdb.Close()
		return a.rollbackRestore(path, backup, marker, err)
	}
	// A restored snapshot may contain sessions that were revoked after backup.
	// Require every browser to sign in again after a successful restore.
	if _, err = a.DB.Exec(ctx, "DELETE FROM sessions"); err != nil {
		return a.rollbackRestore(path, backup, marker, err)
	}
	a.auditBlocked.Store(true)
	if err = a.Restore(ctx); err != nil {
		return a.rollbackRestore(path, backup, marker, err)
	}
	a.RecoverAudit(ctx)
	if ctx.Err() != nil || a.auditBlocked.Load() {
		return a.rollbackRestore(path, backup, marker, errors.New("restored runtime or audit recovery did not complete"))
	}
	if err := os.Remove(marker); err != nil {
		newdb.Close()
		return a.rollbackRestore(path, backup, marker, err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return a.rollbackRestore(path, backup, marker, err)
	}
	return nil
}

func (a *App) resumeOriginal(path, marker string, cause error) error {
	if a.DB != nil {
		a.DB.Close()
	}
	db, err := storage.Open(path)
	if err != nil {
		return fmt.Errorf("original database could not be reopened: %w", err)
	}
	a.DB = db
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	a.auditBlocked.Store(true)
	if err = a.Restore(ctx); err != nil {
		return fmt.Errorf("original database reopened but runtime failed: %w", err)
	}
	a.RecoverAudit(ctx)
	if ctx.Err() != nil {
		return errors.New("original runtime recovery timed out")
	}
	if err = os.Remove(marker); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err = syncDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return cause
}

func (a *App) validateRestoredCurrent(ctx context.Context) (map[string]int64, error) {
	var admins int
	if err := a.DB.QueryRow(ctx, "SELECT count(*) FROM users WHERE role='admin' AND enabled").Scan(&admins); err != nil || admins < 1 {
		return nil, errors.New("no administrator")
	}
	if err := a.VerifySecrets(ctx); err != nil {
		return nil, err
	}
	return nil, nil
}

func (a *App) rollbackRestore(path, backup, marker string, cause error) error {
	a.closeRuntime()
	if a.DB != nil {
		a.DB.Close()
	}
	failed := path + ".restore.failed-" + ID()
	if _, err := os.Stat(path); err == nil {
		if err = os.Rename(path, failed); err != nil {
			return fmt.Errorf("failed database cannot be moved: %w", err)
		}
	}
	if err := removeSQLiteSidecars(path); err != nil {
		return fmt.Errorf("failed database sidecars cannot be removed: %w", err)
	}
	if err := os.Rename(backup, path); err != nil {
		return fmt.Errorf("restore and rollback failed: %w", err)
	}
	db, err := storage.Open(path)
	if err != nil {
		return fmt.Errorf("restore failed and old database reopen failed: %w", err)
	}
	a.DB = db
	a.auditBlocked.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err = a.Restore(ctx); err != nil {
		return fmt.Errorf("original database restored but runtime failed: %w", err)
	}
	a.RecoverAudit(ctx)
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return cause
}

func removeSQLiteSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func syncFileAndDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	f.Close()
	if err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
