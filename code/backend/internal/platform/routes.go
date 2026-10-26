package platform

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/gin-gonic/gin"
)

func (a *App) Handler() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("请求处理异常", "event", "request_panic", "route", c.FullPath(), "detail", fmt.Sprint(recovered), "stack", string(debug.Stack()))
				c.AbortWithStatus(500)
			}
		}()
		c.Next()
		if c.Writer.Status() >= 500 && c.FullPath() != "/api/v1/system/logs" && c.FullPath() != "/healthz" {
			slog.Error("服务请求失败", "event", "request_failed", "route", c.FullPath(), "method", c.Request.Method, "status", c.Writer.Status())
		}
	})
	router.Use(func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Cache-Control", "no-store")
		if a.Personal && personalAccountRoute(c.Request.URL.Path) {
			c.AbortWithStatusJSON(404, gin.H{"error": "个人版不提供账号与角色管理"})
			return
		}
		if c.Request.URL.Path != "/healthz" && strings.HasPrefix(c.Request.URL.Path, "/api/v1/system/restore/") && strings.HasSuffix(c.Request.URL.Path, "/commit") {
			if !a.commitMu.TryLock() {
				c.AbortWithStatusJSON(409, gin.H{"error": "已有恢复请求进行中"})
				return
			}
			defer a.commitMu.Unlock()
			if a.restoring.Load() {
				c.AbortWithStatusJSON(503, gin.H{"error": "恢复维护中，请稍后重试"})
				return
			}
			c.Next()
			return
		}
		if a.restoring.Load() {
			c.AbortWithStatusJSON(503, gin.H{"error": "恢复维护中，请稍后重试"})
			return
		}
		// A stateful MCP GET is a long-lived SSE stream. Its authentication is
		// guarded separately; holding the read lock for its entire lifetime would
		// prevent a database restore from ever entering maintenance mode.
		if (c.Request.URL.Path == "/mcp" || strings.HasPrefix(c.Request.URL.Path, "/downloads/desktop/")) && c.Request.Method == http.MethodGet {
			c.Next()
			return
		}
		a.maintenance.RLock()
		defer a.maintenance.RUnlock()
		if a.restoring.Load() {
			c.AbortWithStatusJSON(503, gin.H{"error": "恢复维护中，请稍后重试"})
			return
		}
		c.Next()
	})
	add := func(method, path string, handler http.HandlerFunc) {
		router.Handle(method, path, func(c *gin.Context) {
			for _, param := range c.Params {
				c.Request.SetPathValue(param.Key, param.Value)
			}
			handler(c.Writer, c.Request)
		})
	}
	add("GET", "/healthz", func(w http.ResponseWriter, r *http.Request) {
		if e := a.DB.Ping(r.Context()); e != nil {
			fail(w, 503, "database unavailable")
			return
		}
		respond(w, 200, map[string]string{"status": "ok"})
	})
	add("GET", "/api/v1/desktop/releases", a.desktopReleases)
	add("GET", "/downloads/desktop/:filename", a.desktopDownload)
	for _, route := range []struct{ method, path string }{{"GET", "/api/v1/system/packages"}, {"POST", "/api/v1/system/packages"}, {"PATCH", "/api/v1/system/packages/:id"}, {"DELETE", "/api/v1/system/packages/:id"}} {
		add(route.method, route.path, a.auth(a.softwarePackages))
	}
	add("GET", "/api/v1/system/server-config", a.auth(a.serverConfiguration))
	add("PATCH", "/api/v1/system/server-config", a.auth(a.serverConfiguration))
	add("GET", "/api/v1/system/package-settings", a.auth(a.softwarePackageSettings))
	add("PATCH", "/api/v1/system/package-settings", a.auth(a.softwarePackageSettings))
	add("GET", "/api/v1/setup", a.setup)
	add("POST", "/api/v1/setup", a.setup)
	add("POST", "/api/v1/login", a.login)
	add("POST", "/api/v1/account/password", a.auth(a.changePassword))
	add("GET", "/api/v1/me", a.auth(func(w http.ResponseWriter, r *http.Request) { respond(w, 200, current(r)) }))
	add("POST", "/api/v1/logout", a.auth(a.logout))
	for _, route := range []struct{ method, path string }{{"GET", "/api/v1/users"}, {"POST", "/api/v1/users"}, {"PATCH", "/api/v1/users/:id"}} {
		add(route.method, route.path, a.auth(a.usersAPI))
	}
	for _, route := range []struct{ method, path string }{{"GET", "/api/v1/instances"}, {"POST", "/api/v1/instances"}, {"GET", "/api/v1/instances/:id"}, {"PATCH", "/api/v1/instances/:id"}, {"DELETE", "/api/v1/instances/:id"}} {
		add(route.method, route.path, a.auth(a.instancesAPI))
	}
	add("GET", "/api/v1/mcp-oauth/callback", a.mcpOAuthCallback)
	for _, method := range []string{"GET", "POST", "DELETE"} {
		add(method, "/api/v1/instances/:id/oauth", a.auth(a.mcpOAuthAPI))
	}
	add("GET", "/api/v1/instances/:id/editor", a.auth(a.editorSecrets))
	add("GET", "/api/v1/instances/:id/postgres", a.auth(a.postgresAPI))
	add("POST", "/api/v1/instances/:id/postgres", a.auth(a.postgresAPI))
	add("POST", "/api/v1/instances/:id/:action", a.auth(a.lifecycle))
	add("POST", "/api/v1/instances/test", a.auth(a.testInstance))
	add("GET", "/api/v1/instances/:id/tools", a.auth(a.toolsAPI))
	for _, route := range []struct{ method, path string }{{"GET", "/api/v1/tokens"}, {"POST", "/api/v1/tokens"}, {"DELETE", "/api/v1/tokens/:id"}} {
		add(route.method, route.path, a.auth(a.tokensAPI))
	}
	for _, route := range []struct{ method, path string }{{"GET", "/api/v1/rules"}, {"POST", "/api/v1/rules"}, {"DELETE", "/api/v1/rules/:id"}} {
		add(route.method, route.path, a.auth(a.rulesAPI))
	}
	add("GET", "/api/v1/logs", a.auth(a.logsAPI))
	add("GET", "/api/v1/tasks", a.auth(a.tasksAPI))
	add("GET", "/api/v1/tasks/:id", a.auth(a.tasksAPI))
	add("GET", "/api/v1/client-integrations/targets", a.auth(a.clientIntegrationTargets))
	add("GET", "/api/v1/client-integrations", a.auth(a.listClientIntegrations))
	add("POST", "/api/v1/client-integrations/preview", a.auth(a.previewClientIntegration))
	add("POST", "/api/v1/client-integrations", a.auth(a.applyClientIntegration))
	add("DELETE", "/api/v1/client-integrations/:id", a.auth(a.removeClientIntegration))
	add("GET", "/api/v1/clients", a.auth(a.tokensAPI))
	add("DELETE", "/api/v1/clients/:id", a.auth(a.deleteClient))
	for _, route := range []struct{ method, path string }{{"GET", "/api/v1/roles"}, {"POST", "/api/v1/roles"}, {"PATCH", "/api/v1/roles/:id"}, {"DELETE", "/api/v1/roles/:id"}} {
		add(route.method, route.path, a.auth(a.rolesAPI))
	}
	add("GET", "/api/v1/gateway", a.auth(func(w http.ResponseWriter, r *http.Request) {
		origin := a.PublicOrigin()
		respond(w, 200, map[string]any{"url": origin + "/mcp", "template": map[string]any{"mcpServers": map[string]any{"toolbox": map[string]any{"url": origin + "/mcp", "headers": map[string]string{"Authorization": "Bearer <TOKEN>"}}}}})
	}))
	add("GET", "/api/v1/catalog", a.auth(a.catalog))
	add("POST", "/api/v1/install", a.auth(a.install))
	add("POST", "/api/v1/install/offline", a.auth(a.installOffline))
	add("GET", "/api/v1/system/catalog-registry", a.auth(a.systemCatalogRegistry))
	add("POST", "/api/v1/system/catalog-registry", a.auth(a.systemCatalogRegistry))
	add("POST", "/api/v1/scan", a.auth(a.scan))
	add("GET", "/api/v1/scan/sources", a.auth(a.scanSources))
	add("POST", "/api/v1/discoveries/:id/import", a.auth(a.importDiscovery))
	add("POST", "/api/v1/discoveries/batch-import", a.auth(a.batchImportDiscoveries))
	add("GET", "/api/v1/discoveries", a.auth(a.discoveries))
	add("GET", "/api/v1/system/logs", a.auth(a.systemLogs))
	add("GET", "/api/v1/system/status", a.auth(a.systemStatus))
	add("POST", "/api/v1/system/health-check", a.auth(a.systemHealthCheck))
	add("POST", "/api/v1/system/native-stdio", a.auth(a.systemNativeStdio))
	add("GET", "/api/v1/system/scan-roots", a.auth(a.scanRootsAPI))
	add("POST", "/api/v1/system/scan-roots", a.auth(a.scanRootsAPI))
	add("DELETE", "/api/v1/system/scan-roots", a.auth(a.scanRootsAPI))
	add("POST", "/api/v1/system/listener", a.auth(a.systemListener))
	add("POST", "/api/v1/system/backup", a.auth(a.systemBackup))
	add("POST", "/api/v1/system/restore", a.auth(a.systemRestoreUpload))
	add("POST", "/api/v1/system/restore/:id/commit", a.auth(a.systemRestoreCommit))
	router.Any("/mcp", func(c *gin.Context) { a.Gateway(c.Writer, c.Request) })
	return router
}
