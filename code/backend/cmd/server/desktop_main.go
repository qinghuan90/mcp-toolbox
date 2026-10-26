//go:build desktop

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"toolbox/internal/desktoptray"
	"toolbox/internal/platform"
	"toolbox/internal/runlog"
)

type desktopApp struct {
	root       string
	startupMu  sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	ready      chan struct{}
	done       chan struct{}
	readyOnce  sync.Once
	mu         sync.RWMutex
	service    *platform.App
	startError error
	quitting   atomic.Bool
	apiHandler http.Handler
}

func main() {
	dataDir := flag.String("data-dir", "", "independent local desktop data directory")
	flag.Parse()
	root, err := desktopEnvironment(*dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	logStore, err := runlog.New(filepath.Join(root, "data", "runtime-logs"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	slog.SetDefault(slog.New(logStore.Handler(slog.NewTextHandler(os.Stderr, nil))))
	slog.Info("桌面应用启动", "event", "desktop_starting")

	d := &desktopApp{root: root, ready: make(chan struct{}), done: make(chan struct{})}
	assets, err := fs.Sub(webdist, "webdist")
	if err != nil {
		slog.Error("desktop assets unavailable")
		return
	}
	m := menu.NewMenu()
	appMenu := m.AddSubmenu("Toolbox")
	appMenu.AddText("显示主窗口", keys.CmdOrCtrl("0"), func(*menu.CallbackData) { d.show() })
	appMenu.AddText("隐藏窗口", keys.CmdOrCtrl("h"), func(*menu.CallbackData) {
		if d.ctx != nil {
			wruntime.WindowHide(d.ctx)
		}
	})
	appMenu.AddSeparator()
	appMenu.AddText("退出 Toolbox", keys.CmdOrCtrl("q"), func(*menu.CallbackData) { d.quit() })
	m.Append(menu.EditMenu())
	identity := sha256.Sum256([]byte(root))
	err = wails.Run(&options.App{
		Title: "Toolbox · 本地版", Width: 1380, Height: 900, MinWidth: 900, MinHeight: 620,
		BackgroundColour: options.NewRGBA(245, 247, 246, 255), Menu: m,
		HideWindowOnClose:  desktoptray.Supported(),
		Bind:               []interface{}{&desktopFiles{app: d}},
		AssetServer:        &assetserver.Options{Assets: assets, Handler: desktopPageFallback(assets), Middleware: d.middleware},
		SingleInstanceLock: &options.SingleInstanceLock{UniqueId: "toolbox-desktop-" + hex.EncodeToString(identity[:12]), OnSecondInstanceLaunch: func(options.SecondInstanceData) { d.show() }},
		OnStartup: func(ctx context.Context) {
			d.ctx = ctx
			go func() {
				files := &desktopFiles{app: d}
				if state, err := files.GetStartup(); err == nil && state.Enabled {
					if _, err = files.SetStartup(true); err != nil {
						slog.Warn("startup application refresh failed", "error", err)
					}
				}
			}()
			serviceCtx, cancel := context.WithCancel(context.Background())
			d.cancel = cancel
			desktoptray.Start(func(action int) {
				if action == 1 {
					d.show()
				} else if action == 2 {
					d.quit()
				}
			})
			go func() {
				defer close(d.done)
				err := runServer(serviceCtx, serverOptions{LogStore: logStore, Host: "127.0.0.1", Personal: true, FallbackPort: true, Ready: func(app *platform.App) {
					d.mu.Lock()
					d.service = app
					d.apiHandler = app.PersonalHandler()
					d.mu.Unlock()
					d.readyOnce.Do(func() { close(d.ready) })
				}})
				d.mu.Lock()
				d.startError = err
				d.mu.Unlock()
				d.readyOnce.Do(func() { close(d.ready) })
				if err != nil {
					slog.Error("local desktop service stopped", "error", err)
					_, _ = wruntime.MessageDialog(ctx, wruntime.MessageDialogOptions{Type: wruntime.ErrorDialog, Title: "本地服务无法启动", Message: err.Error(), Buttons: []string{"关闭"}})
					d.quit()
				}
			}()
		},
		OnBeforeClose: func(ctx context.Context) bool {
			// Window hiding is handled by the native window delegate. This hook
			// also receives Dock / system termination requests, which must exit.
			d.quitting.Store(true)
			slog.Info("desktop exit requested")
			return false
		},
		OnShutdown: func(context.Context) {
			desktoptray.Stop()
			if d.cancel != nil {
				d.cancel()
			}
			select {
			case <-d.done:
			case <-time.After(20 * time.Second):
				slog.Error("desktop service shutdown timed out")
			}
		},
	})
	if err != nil {
		slog.Error("desktop window failed", "error", err)
	}
}

func (d *desktopApp) show() {
	if d.ctx != nil && !d.quitting.Load() {
		wruntime.Show(d.ctx)
		wruntime.WindowShow(d.ctx)
		wruntime.WindowUnminimise(d.ctx)
	}
}
func (d *desktopApp) quit() {
	if d.ctx != nil {
		if desktoptray.RequestQuit() {
			return
		}
		wruntime.Quit(d.ctx)
	}
}

// Personal administrator access stays inside this Wails bridge; the loopback
// TCP listener never receives the private request context or a login cookie.
func (d *desktopApp) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" && origin != "null" && origin != "wails://wails" && origin != "http://wails.localhost" && origin != "https://wails.localhost" {
			http.Error(w, "不允许的桌面请求来源", 403)
			return
		}
		select {
		case <-d.ready:
		case <-r.Context().Done():
			return
		case <-time.After(90 * time.Second):
			desktopProxyError(w, "本地服务仍在启动，请稍后重试")
			return
		}
		d.mu.RLock()
		app, handler, startErr := d.service, d.apiHandler, d.startError
		d.mu.RUnlock()
		if app == nil || handler == nil || startErr != nil {
			desktopProxyError(w, "本地服务无法启动，请查看应用数据目录 data/runtime-logs 中的异常日志")
			return
		}
		target, err := url.Parse(app.PublicOrigin())
		if err != nil {
			desktopProxyError(w, "本地服务地址无效")
			return
		}
		request := r.Clone(r.Context())
		request.Host = target.Host
		request.Header.Set("Origin", target.String())
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		request.Header.Del("Cookie")
		handler.ServeHTTP(w, request)
	})
}
func desktopProxyError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(503)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func desktopEnvironment(root string) (string, error) {
	if root == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(base, "Toolbox Desktop")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("桌面数据目录必须为本机普通目录")
	}
	if err = os.Chmod(root, 0700); err != nil {
		return "", err
	}
	// A desktop launch must never inherit the enterprise service's credentials,
	// database path, listener, TLS settings, or forced runtime permissions.
	for _, key := range []string{"MASTER_KEY", "SETUP_KEY", "PUBLIC_URL", "LISTEN_ADDR", "TLS_CERT_FILE", "TLS_KEY_FILE", "COOKIE_SECURE", "ACCESS_CIDRS", "ALLOW_UNSANDBOXED_STDIO", "SCAN_ROOTS"} {
		_ = os.Unsetenv(key)
	}
	_ = os.Setenv("SQLITE_PATH", filepath.Join(root, "data", "toolbox.sqlite"))
	_ = os.Setenv("TOOLBOX_SECRETS_FILE", filepath.Join(root, "local.env"))
	_ = os.Setenv("ARTIFACT_DIR", filepath.Join(root, "data", "artifacts"))
	_ = os.Setenv("AUDIT_DIR", filepath.Join(root, "data", "audit"))
	_ = os.Unsetenv("TOOLBOX_DESKTOP_RELEASES_DIR")
	home, _ := os.UserHomeDir()
	paths := []string{os.Getenv("PATH"), "/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, ".local", "bin"), filepath.Join(home, ".volta", "bin"), "/usr/bin", "/bin", "/usr/sbin", "/sbin"}
	_ = os.Setenv("PATH", strings.Join(paths, string(os.PathListSeparator)))
	return root, nil
}

func desktopPageFallback(assets fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || path.Ext(r.URL.Path) != "" {
			http.NotFound(w, r)
			return
		}
		content, err := fs.ReadFile(assets, "index.html")
		if err != nil {
			http.Error(w, "页面资源不可用", 503)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(content)
	})
}
