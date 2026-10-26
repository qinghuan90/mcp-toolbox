package main

import (
	"context"
	"crypto/tls"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"toolbox/internal/runlog"

	"github.com/gin-gonic/gin"
	"toolbox/internal/platform"
	"toolbox/internal/storage"
)

//go:embed all:webdist
var webdist embed.FS

type serverOptions struct {
	Config       *runtimeConfigStore
	LogStore     *runlog.Store
	Host         string
	Port         int
	ManualPort   bool
	FallbackPort bool
	Personal     bool
	Ready        func(*platform.App)
}

// Both the CLI and Wails own this same service lifecycle and business implementation.
func runServer(ctx context.Context, cfg serverOptions) (result error) {
	dataPath := os.Getenv("SQLITE_PATH")
	if dataPath == "" {
		if cfg.Config != nil {
			dataPath = cfg.Config.resolved(cfg.Config.active.DatabasePath)
		} else {
			dataPath = "data/toolbox.sqlite"
		}
	}
	store := cfg.LogStore
	if store == nil {
		var err error
		store, err = runlog.New(filepath.Join(filepath.Dir(dataPath), "runtime-logs"))
		if err != nil {
			return err
		}
		slog.SetDefault(slog.New(store.Handler(slog.NewTextHandler(os.Stderr, nil))))
	}
	logCtx, stopLogs := context.WithCancel(ctx)
	defer stopLogs()
	go store.RunPruner(logCtx)
	slog.Info("服务开始启动", "event", "service_starting")
	defer func() {
		if p := recover(); p != nil {
			slog.Error("服务启动异常", "event", "service_panic", "detail", fmt.Sprint(p))
			panic(p)
		}
		if result != nil {
			slog.Error("服务启动或运行失败", "event", "service_failed", "detail", result.Error())
		} else {
			slog.Info("服务已停止", "event", "service_stopped")
		}
	}()
	settingsPath := pathForListenerSettings(dataPath)
	defaultHost, defaultPort := "127.0.0.1", 8090
	if cfg.Config != nil {
		defaultHost, defaultPort = cfg.Config.active.ListenHost, cfg.Config.active.ListenPort
	}
	if storedPort, err := readListenerPort(settingsPath); cfg.Config == nil && err != nil {
		return err
	} else if cfg.Config == nil && storedPort != 0 {
		defaultPort = storedPort
	}
	if configured := os.Getenv("LISTEN_ADDR"); configured != "" {
		host, portText, err := net.SplitHostPort(configured)
		if err != nil {
			return err
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			return err
		}
		defaultHost, defaultPort = host, port
	}
	host, port := defaultHost, defaultPort
	if cfg.Host != "" {
		host = cfg.Host
	}
	if cfg.Port != 0 {
		port = cfg.Port
	}
	if port < 1 || port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	flagOverride := cfg.ManualPort
	address := net.JoinHostPort(host, strconv.Itoa(port))
	accessCIDRs, useTLS, err := validateListen(address)
	if err != nil {
		return err
	}
	if cfg.Personal && (host != "127.0.0.1" || useTLS) {
		return errors.New("personal edition must listen on the local loopback address")
	}
	explicitPublicURL := os.Getenv("PUBLIC_URL") != ""
	if !explicitPublicURL {
		os.Setenv("PUBLIC_URL", "http://"+address)
	}
	if !useTLS && os.Getenv("COOKIE_SECURE") == "" && strings.HasPrefix(os.Getenv("PUBLIC_URL"), "http://") {
		os.Setenv("COOKIE_SECURE", "false")
	}
	portEditable := !flagOverride && os.Getenv("LISTEN_ADDR") == "" && !explicitPublicURL && !useTLS
	explicitSetupKey := os.Getenv("SETUP_KEY") != ""
	dataPath, unlockDatabase, err := lockDatabase(dataPath)
	if err != nil {
		return err
	}
	defer unlockDatabase()
	if err := storage.RecoverPending(dataPath); err != nil {
		return err
	}
	if err := ensureLocalSecrets(dataPath); err != nil {
		return err
	}
	db, err := storage.Open(dataPath)
	if err != nil {
		return err
	}
	app, err := platform.New(db)
	if err != nil {
		return err
	}
	app.RunLogs = store
	app.LocalOnly = !useTLS
	app.Personal = cfg.Personal
	if cfg.Config != nil {
		app.PackageDirectory = cfg.Config.packageDirectory
		app.PackageDirectoryEditable = cfg.Config.overrides["package_directory"] == ""
		app.ChangePackageDirectory = cfg.Config.changePackageDirectory
		app.ServerConfigSnapshot = cfg.Config.snapshot
		app.SaveServerConfiguration = cfg.Config.save
	}
	defer func() { app.DB.Close() }()
	if err = app.VerifySecrets(ctx); err != nil {
		return err
	}
	if err = app.Restore(ctx); err != nil {
		return err
	}
	if cfg.Personal {
		if err = app.BootstrapPersonalOwner(ctx); err != nil {
			return err
		}
	} else if !useTLS && !explicitSetupKey {
		if err = app.BootstrapLocalAdmin(ctx); err != nil {
			return err
		}
	}
	app.RecoverAudit(ctx)
	go app.RunAuditRecovery(ctx)
	app.StartHealthChecks(ctx)
	defer app.Close()
	router := app.Handler()
	attachWeb(router)
	newServer := func(addr string) *http.Server {
		return &http.Server{Addr: addr, Handler: restrictClients(router, accessCIDRs), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	}
	server := newServer(address)
	var listener net.Listener
	if !useTLS {
		listener, err = net.Listen("tcp", address)
		if err != nil && cfg.FallbackPort {
			listener, err = net.Listen("tcp", net.JoinHostPort(host, "0"))
			if err == nil {
				port = listener.Addr().(*net.TCPAddr).Port
				address = net.JoinHostPort(host, strconv.Itoa(port))
				if err = writeListenerPort(settingsPath, port); err != nil {
					listener.Close()
					return err
				}
				app.SetPublicOrigin("http://" + address)
				server = newServer(address)
			}
		}
		if err != nil {
			return err
		}
	}
	var listenerMu sync.Mutex
	currentServer, currentListener, currentPort := server, listener, port
	serverErrors := make(chan error, 2)
	serve := func(s *http.Server, l net.Listener) {
		go func() {
			serveErr := s.Serve(l)
			if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && !errors.Is(serveErr, net.ErrClosed) {
				select {
				case serverErrors <- serveErr:
				default:
				}
			}
		}()
	}
	app.ListenerPort = func() int { listenerMu.Lock(); defer listenerMu.Unlock(); return currentPort }
	app.PortEditable = portEditable
	if portEditable {
		app.ChangePort = func(nextPort int) (string, error) {
			listenerMu.Lock()
			defer listenerMu.Unlock()
			if nextPort == currentPort {
				return app.PublicOrigin(), nil
			}
			nextAddress := net.JoinHostPort(host, strconv.Itoa(nextPort))
			nextListener, listenErr := net.Listen("tcp", nextAddress)
			if listenErr != nil {
				return "", errors.New("端口不可用，请选择其他端口")
			}
			persistPort := func() error {
				if cfg.Config != nil {
					return cfg.Config.changePort(nextPort)
				}
				return writeListenerPort(settingsPath, nextPort)
			}
			if writeErr := persistPort(); writeErr != nil {
				nextListener.Close()
				return "", errors.New("保存端口配置失败")
			}
			previousServer, previousListener := currentServer, currentListener
			currentServer, currentListener, currentPort = newServer(nextAddress), nextListener, nextPort
			app.SetPublicOrigin("http://" + nextAddress)
			serve(currentServer, currentListener)
			slog.Info("toolbox listener changed", "address", nextAddress)
			time.AfterFunc(3*time.Second, func() {
				previousListener.Close()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				previousServer.Shutdown(shutdownCtx)
			})
			return app.PublicOrigin(), nil
		}
	}
	slog.Info("toolbox started", "address", address, "storage", "sqlite", "tls", useTLS)
	if useTLS {
		go func() {
			serverErrors <- server.ListenAndServeTLS(os.Getenv("TLS_CERT_FILE"), os.Getenv("TLS_KEY_FILE"))
		}()
	} else {
		serve(server, listener)
	}
	if cfg.Ready != nil {
		cfg.Ready(app)
	}
	select {
	case <-ctx.Done():
		listenerMu.Lock()
		active := currentServer
		listenerMu.Unlock()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		active.Shutdown(shutdownCtx)
	case err = <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return nil
}

func validateListen(address string) ([]*net.IPNet, bool, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, false, err
	}
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return nil, false, nil
	}
	if os.Getenv("COOKIE_SECURE") == "false" {
		return nil, false, errors.New("non-loopback listener requires secure cookies")
	}
	publicURL, err := url.Parse(os.Getenv("PUBLIC_URL"))
	if err != nil || publicURL.Scheme != "https" || publicURL.Host == "" || publicURL.User != nil || (publicURL.Path != "" && publicURL.Path != "/") || publicURL.RawQuery != "" || publicURL.Fragment != "" {
		return nil, false, errors.New("non-loopback listener requires a root HTTPS PUBLIC_URL")
	}
	certFile, keyFile := os.Getenv("TLS_CERT_FILE"), os.Getenv("TLS_KEY_FILE")
	if certFile == "" || keyFile == "" {
		return nil, false, errors.New("non-loopback listener requires TLS_CERT_FILE and TLS_KEY_FILE")
	}
	if _, err = tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		return nil, false, err
	}
	if os.Getenv("ACCESS_CIDRS") == "" {
		return nil, false, errors.New("non-loopback listener requires ACCESS_CIDRS")
	}
	var allowed []*net.IPNet
	for _, entry := range strings.Split(os.Getenv("ACCESS_CIDRS"), ",") {
		_, network, parseErr := net.ParseCIDR(strings.TrimSpace(entry))
		if parseErr != nil || network == nil {
			return nil, false, errors.New("invalid ACCESS_CIDRS")
		}
		prefix, _ := network.Mask.Size()
		if prefix == 0 {
			return nil, false, errors.New("ACCESS_CIDRS cannot include all addresses")
		}
		allowed = append(allowed, network)
	}
	return allowed, true, nil
}

func restrictClients(next http.Handler, allowed []*net.IPNet) http.Handler {
	if len(allowed) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "client address unavailable", http.StatusForbidden)
			return
		}
		ip := net.ParseIP(host)
		if ip == nil {
			http.Error(w, "client address unavailable", http.StatusForbidden)
			return
		}
		for _, network := range allowed {
			if network.Contains(ip) {
				next.ServeHTTP(w, r)
				return
			}
		}
		http.Error(w, "client address not allowed", http.StatusForbidden)
	})
}

func attachWeb(router *gin.Engine) {
	web, err := fs.Sub(webdist, "webdist")
	if err != nil {
		panic(err)
	}
	assets := http.FileServer(http.FS(web))
	router.GET("/toolbox-icon.png", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=3600")
		assets.ServeHTTP(c.Writer, c.Request)
	})
	router.GET("/assets/*filepath", func(c *gin.Context) {
		if info, err := fs.Stat(web, strings.TrimPrefix(c.Request.URL.Path, "/")); err != nil || info.IsDir() {
			c.Header("Cache-Control", "no-store")
			c.Status(404)
			return
		}
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
		assets.ServeHTTP(c.Writer, c.Request)
	})
	router.NoRoute(func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Status(404)
			return
		}
		if strings.HasPrefix(c.Request.URL.Path, "/api/") || strings.HasPrefix(c.Request.URL.Path, "/mcp") || path.Ext(c.Request.URL.Path) != "" {
			c.Status(404)
			return
		}
		content, err := fs.ReadFile(web, "index.html")
		if err != nil {
			c.Status(503)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Data(200, "text/html; charset=utf-8", content)
	})
}
