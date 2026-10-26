package platform

import (
	"context"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type postgresConnection struct {
	Name               string `json:"name"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Database           string `json:"database"`
	Username           string `json:"username"`
	SSLMode            string `json:"sslmode"`
	PasswordConfigured bool   `json:"password_configured"`
	PasswordMode       string `json:"password_mode,omitempty"`
	Password           string `json:"password,omitempty"`
}

// This adapter replaces one positional PostgreSQL URI. Each connection owns an
// independent MCP process because server-postgres accepts one database per process.
func postgresArgument(i Instance) (int, *url.URL, bool) {
	if i.Transport != "stdio" {
		return 0, nil, false
	}
	index := -1
	var result *url.URL
	for n, arg := range i.Args {
		if !strings.HasPrefix(arg, "postgres://") && !strings.HasPrefix(arg, "postgresql://") {
			continue
		}
		u, err := url.Parse(arg)
		if err != nil || u.Hostname() == "" || index >= 0 {
			return 0, nil, false
		}
		index, result = n, u
	}
	return index, result, index >= 0
}
func isReferencePostgres(i Instance) bool {
	if _, _, ok := postgresArgument(i); !ok {
		return false
	}
	for _, arg := range i.Args {
		if arg == "@modelcontextprotocol/server-postgres" || strings.HasPrefix(arg, "@modelcontextprotocol/server-postgres@") || strings.Contains(arg, "/@modelcontextprotocol/server-postgres/") {
			return true
		}
	}
	return false
}
func postgresView(i Instance, u *url.URL) postgresConnection {
	port := 5432
	if u.Port() != "" {
		port, _ = strconv.Atoi(u.Port())
	}
	v := postgresConnection{Name: i.Name, Host: u.Hostname(), Port: port, Database: strings.TrimPrefix(u.Path, "/"), SSLMode: u.Query().Get("sslmode")}
	if u.User != nil {
		v.Username = u.User.Username()
	}
	q := u.Query()
	if q.Get("host") != "" {
		v.Host = q.Get("host")
	}
	if q.Get("port") != "" {
		v.Port, _ = strconv.Atoi(q.Get("port"))
	}
	if q.Get("user") != "" {
		v.Username = q.Get("user")
	}
	_, v.PasswordConfigured = postgresPassword(u)
	return v
}

// applyPostgresConnection changes only the URI while preserving unrelated arguments.
func applyPostgresConnection(i *Instance, connection postgresConnection) error {
	index, base, ok := postgresArgument(*i)
	if !ok {
		return errors.New("此配置不包含单个 PostgreSQL URI，请检查启动参数")
	}
	connection.Name = i.Name
	value, err := postgresURI(base, connection)
	if err != nil {
		return err
	}
	i.Args = append([]string(nil), i.Args...)
	i.Args[index] = value
	return nil
}
func postgresPassword(u *url.URL) (string, bool) {
	if password := u.Query().Get("password"); password != "" {
		return password, true
	}
	if u.User != nil {
		return u.User.Password()
	}
	return "", false
}
func postgresURI(base *url.URL, in postgresConnection) (string, error) {
	in.Host = strings.TrimSpace(in.Host)
	if in.Host == "" || strings.ContainsAny(in.Host, " /@?#\\\t\r\n") || in.Port < 1 || in.Port > 65535 || strings.TrimSpace(in.Database) == "" || strings.TrimSpace(in.Username) == "" || len(in.Name) > 100 || strings.TrimSpace(in.Name) == "" {
		return "", errors.New("请填写连接名称、有效主机、端口、数据库和用户名")
	}
	switch in.SSLMode {
	case "", "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		return "", errors.New("SSL 模式无效")
	}
	var password string
	configured := false
	switch in.PasswordMode {
	case "keep":
		password, configured = postgresPassword(base)
	case "replace":
		password = in.Password
		configured = true
	case "none":
	default:
		return "", errors.New("请选择密码处理方式")
	}
	u := *base
	u.Host = net.JoinHostPort(strings.Trim(in.Host, "[]"), strconv.Itoa(in.Port))
	u.Path = "/" + in.Database
	u.RawPath = ""
	u.Fragment = ""
	u.RawFragment = ""
	u.User = url.User(in.Username)
	if configured {
		u.User = url.UserPassword(in.Username, password)
	}
	query := u.Query()
	for _, key := range []string{"host", "port", "user", "password", "database", "ssl", "sslmode"} {
		query.Del(key)
	}
	if in.SSLMode != "" {
		query.Set("sslmode", in.SSLMode)
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}
func (a *App) postgresAPI(w http.ResponseWriter, r *http.Request) {
	if current(r).Role != "admin" {
		fail(w, 403, "需要管理员权限")
		return
	}
	id := r.PathValue("id")
	l := a.lock(id)
	l.Lock()
	defer l.Unlock()
	base, err := a.loadInstance(r.Context(), id)
	if err != nil {
		fail(w, 404, "实例不存在")
		return
	}
	index, uri, supported := postgresArgument(base)
	if r.Method == "GET" {
		if !supported {
			respond(w, 200, map[string]any{"supported": false})
			return
		}
		respond(w, 200, map[string]any{"supported": true, "version": base.Version, "connection": postgresView(base, uri)})
		return
	}
	if !supported {
		fail(w, 400, "此实例未使用单个 PostgreSQL 连接 URI 参数，请使用通用配置")
		return
	}
	var in struct {
		Version     int                  `json:"version"`
		Connections []postgresConnection `json:"connections"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Version != base.Version {
		fail(w, 409, "配置版本冲突，请刷新后重试")
		return
	}
	if len(in.Connections) < 1 || len(in.Connections) > 20 {
		fail(w, 400, "每次可保存 1 至 20 个数据库连接")
		return
	}
	instances := make([]Instance, len(in.Connections))
	ids := make([]string, len(in.Connections))
	names := map[string]bool{}
	for n, c := range in.Connections {
		c.Name = strings.TrimSpace(c.Name)
		value, e := postgresURI(uri, c)
		if e != nil {
			fail(w, 400, e.Error())
			return
		}
		if names[c.Name] {
			fail(w, 400, "连接名称不能重复")
			return
		}
		names[c.Name] = true
		i := base
		i.Name = c.Name
		i.Args = append([]string(nil), base.Args...)
		i.Args[index] = value
		if n == 0 {
			i.Version++
		} else {
			i.ID = ID()
			i.Version = 1
			i.Enabled = false
			i.Status = "stopped"
			i.LastError = ""
			i.Runtime = nil
		}
		instances[n] = i
		ids[n] = i.ID
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		fail(w, 503, "数据库不可用")
		return
	}
	defer tx.Rollback(r.Context())
	for n, i := range instances {
		b, sec := configBytes(i), a.seal(instanceSecrets(i))
		if n == 0 {
			result, updateErr := tx.Exec(r.Context(), "UPDATE instances SET name=$2,config=$3,secret=$4,version=$5 WHERE id=$1 AND version=$6", i.ID, i.Name, b, sec, i.Version, base.Version)
			e = updateErr
			if e == nil && result.RowsAffected() != 1 {
				fail(w, 409, "配置版本冲突，请刷新后重试")
				return
			}
		} else {
			_, e = tx.Exec(r.Context(), "INSERT INTO instances(id,name,transport,config,secret) VALUES($1,$2,$3,$4,$5)", i.ID, i.Name, i.Transport, b, sec)
		}
		if e == nil {
			_, e = tx.Exec(r.Context(), "INSERT INTO config_versions VALUES($1,$2,$3,$4)", i.ID, i.Version, b, sec)
		}
		if e != nil {
			fail(w, 503, "保存数据库连接失败")
			return
		}
	}
	if tx.Commit(r.Context()) != nil {
		fail(w, 503, "保存数据库连接失败")
		return
	}
	taskID, startErr := a.enqueueTask(r.Context(), "postgres_start", func(ctx context.Context) (any, error) { return a.runPostgresStarts(ctx, ids) })
	respond(w, 200, map[string]any{"instance_ids": ids, "start_task_id": taskID, "start_error": taskError(startErr)})
}
func (a *App) runPostgresStarts(ctx context.Context, ids []string) (any, error) {
	results := []map[string]string{}
	ready := 0
	failed := 0
	for _, id := range ids {
		l := a.lock(id)
		l.Lock()
		func() {
			defer l.Unlock()
			_, hadOld := a.Runtime.Load(id)
			startCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			defer cancel()
			status := "fault"
			_, e := a.DB.Exec(startCtx, "UPDATE instances SET enabled=true WHERE id=$1", id)
			if e == nil {
				e = a.startPostgres(startCtx, id)
			}
			if e == nil {
				ready++
				status = "ready"
			} else {
				failed++
				detail := "数据库连接启动失败，请检查连接配置、凭据与运行依赖"
				if hadOld {
					status = "ready"
					detail = "新配置连接失败，原运行连接继续提供服务；请修正后重试"
				}
				faultCtx, faultCancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer faultCancel()
				a.DB.Exec(faultCtx, "UPDATE instances SET status=$2,last_error=$3 WHERE id=$1", id, status, detail)
			}
			results = append(results, map[string]string{"id": id, "status": status, "configuration_status": map[bool]string{true: "applied", false: "failed"}[e == nil]})
		}()
	}
	result := map[string]any{"instance_ids": ids, "total": len(ids), "ready": ready, "failed": failed, "items": results}
	if failed > 0 {
		return result, errors.New("部分数据库连接未生效，请查看实例状态；已有连接在新配置失败时继续运行")
	}
	return result, nil
}

// server-postgres connects lazily; validate PostgreSQL before publishing a new runtime.
func (a *App) startPostgres(ctx context.Context, id string) (result error) {
	i, err := a.loadInstance(ctx, id)
	if err != nil {
		return err
	}
	h := newInstanceHealth(i)
	defer func() {
		if result != nil {
			if h.Protocol.State != "passed" {
				h.Protocol = stage("failed", redactStartupDiagnostic(i, result.Error()))
			}
			a.failedHealth(i, h)
		}
	}()
	session, pid, err := a.connectInstance(ctx, i)
	if err != nil {
		return fmt.Errorf("MCP 初始化失败：%w", err)
	}
	published := false
	defer func() {
		if !published {
			closeClientSession(session, pid)
		}
	}()
	tools := []*mcp.Tool{}
	found := false
	for tool, e := range session.Tools(ctx, nil) {
		if e != nil {
			return errors.New("工具发现失败")
		}
		tools = append(tools, tool)
		if tool.Name == "query" {
			found = true
		}
	}
	h.connected(i, len(tools))
	if !found {
		h.Business = stage("failed", "未提供业务探针所需的 query 工具")
		return errors.New("此 PostgreSQL 服务未提供 query 工具")
	}
	if err := verifyPostgres(ctx, session); err != nil {
		h.Business = stage("failed", redactStartupDiagnostic(i, err.Error()))
		return err
	}
	h.Business = stage("passed", "PostgreSQL 只读连接查询已通过；不代表所有工具与 SQL 已验证")
	candidate := &snapshot{health: h, generation: ID(), instance: i, session: session, tools: tools, slots: make(chan struct{}, i.Concurrency), startedAt: time.Now(), pid: pid}
	if prior, ok := a.Runtime.Load(id); ok {
		candidate.restartCount = prior.(*snapshot).restartCount + 1
	}
	err = a.publishSnapshot(id, candidate, func() error {
		_, e := a.DB.Exec(ctx, "UPDATE instances SET status='ready',last_error='',runtime_generation=$2 WHERE id=$1", id, candidate.generation)
		return e
	})
	if err != nil {
		return err
	}
	published = true
	a.closeInstanceLeases(id)
	return nil
}

// The reference server connects lazily; a read-only query is required, not just ping.
func verifyPostgres(ctx context.Context, session *mcp.ClientSession) error {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "query", Arguments: map[string]any{"sql": "SELECT current_database() AS database"}})
	if err != nil {
		return fmt.Errorf("PostgreSQL 只读连接验证失败：%w", err)
	}
	if result == nil {
		return errors.New("PostgreSQL 连接验证返回空结果")
	}
	if result.IsError {
		var detail []string
		for _, content := range result.Content {
			if text, ok := content.(*mcp.TextContent); ok {
				detail = append(detail, text.Text)
			}
		}
		return fmt.Errorf("PostgreSQL 只读连接验证失败：%s", strings.Join(detail, " "))
	}
	return nil
}
