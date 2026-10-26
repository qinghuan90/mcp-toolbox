//go:build desktop

package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Native file dialogs are user initiated. Client configuration writes still
// go through the authenticated administrator API with preview and backup.
type desktopFiles struct{ app *desktopApp }

func (f *desktopFiles) SaveBackup() (bool, error) {
	d := f.app
	filename, err := wruntime.SaveFileDialog(d.ctx, wruntime.SaveDialogOptions{
		Title: "保存数据库备份", DefaultFilename: "toolbox-backup.sqlite",
		Filters: []wruntime.FileFilter{{DisplayName: "SQLite 数据库", Pattern: "*.sqlite"}},
	})
	if err != nil || filename == "" {
		return false, err
	}
	d.mu.RLock()
	app, handler, startErr := d.service, d.apiHandler, d.startError
	d.mu.RUnlock()
	if app == nil || handler == nil || startErr != nil {
		return false, errors.New("本地服务尚未就绪")
	}
	origin := app.PublicOrigin()
	request, err := http.NewRequestWithContext(d.ctx, http.MethodPost, origin+"/api/v1/system/backup", nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("Origin", origin)
	file, err := os.CreateTemp(filepath.Dir(filename), ".toolbox-backup-*")
	if err != nil {
		return false, err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	response := &backupResponse{header: make(http.Header), file: file}
	handler.ServeHTTP(response, request)
	if response.status != http.StatusOK || response.err != nil {
		file.Close()
		return false, fmt.Errorf("备份保存失败 (%d)，请检查本地服务和剩余空间", response.status)
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return false, err
	}
	if err = file.Close(); err != nil {
		return false, err
	}
	if err = os.Rename(temporary, filename); err != nil {
		return false, err
	}
	return true, nil
}

// Stream the existing backup API to a private temporary file, without sending
// administrator authority over TCP or buffering the database in memory.
type backupResponse struct {
	header http.Header
	file   io.Writer
	status int
	err    error
}

func (w *backupResponse) Header() http.Header { return w.header }
func (w *backupResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *backupResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.status != http.StatusOK {
		return len(p), nil
	}
	n, err := w.file.Write(p)
	if err != nil {
		w.err = err
	}
	return n, err
}

func (f *desktopFiles) SelectClientDirectory() (string, error) {
	if f.app.ctx == nil {
		return "", errors.New("桌面窗口尚未就绪")
	}
	return wruntime.OpenDirectoryDialog(f.app.ctx, wruntime.OpenDialogOptions{Title: "选择客户端配置根目录"})
}

// OpenOAuthBrowser is called only by the explicit authorization button.
func (f *desktopFiles) OpenOAuthBrowser(raw string) error {
	if f.app.ctx == nil {
		return errors.New("桌面窗口尚未就绪")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errors.New("认证页面必须是 HTTPS 地址")
	}
	wruntime.BrowserOpenURL(f.app.ctx, u.String())
	return nil
}
