package platform

import (
	"context"
	"log/slog"

	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

func validate(i *Instance) error {
	if i.Name == "" {
		return errors.New("名称不能为空")
	}
	if i.Transport != "stdio" && i.Transport != "http" {
		return errors.New("transport 必须为 http 或 stdio")
	}
	if i.Transport == "http" {
		u, e := url.Parse(i.URL)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return errors.New("需要有效 HTTP URL，不允许 URL 凭据")
		}
	}
	if i.HeaderHelper != "" {
		if i.Transport != "http" {
			return errors.New("动态请求头仅适用于 HTTP MCP")
		}
		if _, e := parseHelperCommand(i.HeaderHelper); e != nil {
			return errors.New("动态请求头命令格式无效：" + e.Error())
		}
	}
	if i.Transport == "stdio" && i.Command == "" {
		return errors.New("需要命令")
	}
	if i.Concurrency == 0 {
		i.Concurrency = 1
	}
	if i.Concurrency < 1 || i.Concurrency > 20 {
		return errors.New("实例并发应为1–20")
	}
	if i.Timeout == 0 {
		i.Timeout = 60
	}
	if i.Timeout < 1 || i.Timeout > 600 {
		return errors.New("超时应为1–600秒")
	}
	for k := range i.Headers {
		if k == "" {
			return errors.New("Header 名称不能为空")
		}
	}
	return nil
}
func (a *App) instancesAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if r.Method == "GET" {
		if id != "" {
			if !a.canUseInstance(r.Context(), current(r).ID, id) {
				fail(w, 403, "无此 MCP 服务使用权限")
				return
			}
			i, e := a.loadInstance(r.Context(), id)
			if e != nil {
				fail(w, 404, "实例不存在")
				return
			}
			respond(w, 200, a.presentInstance(i))
			return
		}
		rows, e := a.DB.Query(r.Context(), "SELECT id FROM instances ORDER BY created_at DESC")
		if e != nil {
			fail(w, 503, "查询失败")
			return
		}
		ids := []string{}
		for rows.Next() {
			var s string
			rows.Scan(&s)
			ids = append(ids, s)
		}
		rows.Close()
		out := []Instance{}
		for _, s := range ids {
			if !a.canUseInstance(r.Context(), current(r).ID, s) {
				continue
			}
			i, e := a.loadInstance(r.Context(), s)
			if e == nil {
				out = append(out, a.presentInstance(i))
			}
		}
		respond(w, 200, map[string]any{"items": out, "total": len(out)})
		return
	}
	if r.Method == "DELETE" {
		l := a.lock(id)
		l.Lock()
		defer l.Unlock()
		a.stop(id)
		a.invalidateMCPOAuthPending(id)
		tx, e := a.DB.Begin(r.Context())
		if e != nil {
			fail(w, 503, "删除失败")
			return
		}
		defer tx.Rollback(r.Context())
		_, e = tx.Exec(r.Context(), "DELETE FROM instances WHERE id=$1", id)
		if e == nil {
			_, e = tx.Exec(r.Context(), "DELETE FROM system_settings WHERE key=$1", "mcp_oauth:"+id)
		}
		if e == nil {
			e = tx.Commit(r.Context())
		}
		if e != nil {
			fail(w, 503, "删除失败")
			return
		}
		respond(w, 200, map[string]bool{"ok": true})
		return
	}
	var input struct {
		Instance
		PostgresConnection *postgresConnection `json:"postgres_connection,omitempty"`
	}
	if !decode(w, r, &input) {
		return
	}
	in := input.Instance
	if r.Method == "POST" && input.PostgresConnection != nil {
		if e := applyPostgresConnection(&in, *input.PostgresConnection); e != nil {
			fail(w, 400, e.Error())
			return
		}
	}
	if e := validate(&in); e != nil {
		fail(w, 400, e.Error())
		return
	}
	if r.Method == "POST" {
		in.ID = ID()
		in.Version = 1
		in.Enabled = false
		in.Status = "stopped"
		b := configBytes(in)
		sec := a.seal(instanceSecrets(in))
		tx, e := a.DB.Begin(r.Context())
		if e != nil {
			fail(w, 503, "数据库不可用")
			return
		}
		defer tx.Rollback(r.Context())
		_, e = tx.Exec(r.Context(), "INSERT INTO instances(id,name,transport,config,secret) VALUES($1,$2,$3,$4,$5)", in.ID, in.Name, in.Transport, b, sec)
		if e == nil {
			_, e = tx.Exec(r.Context(), "INSERT INTO config_versions VALUES($1,1,$2,$3)", in.ID, b, sec)
		}
		if e != nil || tx.Commit(r.Context()) != nil {
			fail(w, 503, "保存失败")
			return
		}
		taskID, startErr := a.enqueueTask(r.Context(), "instance_start", func(ctx context.Context) (any, error) {
			return a.runImportedStarts(ctx, []string{in.ID})
		})
		respond(w, 201, struct {
			Instance
			StartTaskID string `json:"start_task_id,omitempty"`
			StartError  string `json:"start_error,omitempty"`
		}{publicInstance(in), taskID, taskError(startErr)})
		return
	}
	l := a.lock(id)
	l.Lock()
	defer l.Unlock()
	old, e := a.loadInstance(r.Context(), id)
	if e != nil {
		fail(w, 404, "实例不存在")
		return
	}
	if in.Version != old.Version {
		fail(w, 409, "配置版本冲突，请刷新")
		return
	}
	in = mergeRedacted(in, old)
	if input.PostgresConnection != nil {
		if e := applyPostgresConnection(&in, *input.PostgresConnection); e != nil {
			fail(w, 400, e.Error())
			return
		}
	}
	in.ID = id
	in.Version = old.Version + 1
	in.Enabled = old.Enabled
	in.Status = old.Status
	b := configBytes(in)
	sec := a.seal(instanceSecrets(in))
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		fail(w, 503, "数据库不可用")
		return
	}
	defer tx.Rollback(r.Context())
	_, e = tx.Exec(r.Context(), "UPDATE instances SET name=$2,transport=$3,config=$4,secret=$5,version=$6 WHERE id=$1", id, in.Name, in.Transport, b, sec, in.Version)
	if e == nil {
		_, e = tx.Exec(r.Context(), "INSERT INTO config_versions VALUES($1,$2,$3,$4)", id, in.Version, b, sec)
	}
	if e != nil || tx.Commit(r.Context()) != nil {
		fail(w, 503, "保存失败")
		return
	}
	if input.PostgresConnection != nil || (isReferencePostgres(in) && !reflect.DeepEqual(in.Args, old.Args)) {
		taskID, startErr := a.enqueueTask(r.Context(), "postgres_start", func(ctx context.Context) (any, error) {
			return a.runPostgresStarts(ctx, []string{id})
		})
		respond(w, 200, struct {
			Instance
			StartTaskID string `json:"start_task_id,omitempty"`
			StartError  string `json:"start_error,omitempty"`
		}{publicInstance(in), taskID, taskError(startErr)})
		return
	}
	respond(w, 200, publicInstance(in))
}
func (a *App) lifecycle(w http.ResponseWriter, r *http.Request) {
	id, action := r.PathValue("id"), r.PathValue("action")
	if action != "start" && action != "stop" && action != "restart" {
		fail(w, 404, "操作不存在")
		return
	}
	if _, e := a.loadInstance(r.Context(), id); e != nil {
		fail(w, 404, "实例不存在")
		return
	}
	a.task(w, r, "instance_"+action, func(ctx context.Context) (any, error) {
		l := a.lock(id)
		l.Lock()
		defer l.Unlock()
		if action == "stop" {
			if _, e := a.DB.Exec(ctx, "UPDATE instances SET enabled=false WHERE id=$1", id); e != nil {
				return nil, e
			}
			a.stop(id)
			if _, e := a.DB.Exec(ctx, "UPDATE instances SET status='stopped',last_error='' WHERE id=$1", id); e != nil {
				return nil, e
			}
			return nil, nil
		}
		_, hadOld := a.Runtime.Load(id)
		if action == "start" && hadOld {
			return map[string]string{"status": "ready"}, nil
		}
		if !hadOld {
			if _, e := a.DB.Exec(ctx, "UPDATE instances SET enabled=true,status='starting' WHERE id=$1", id); e != nil {
				return nil, e
			}
		}
		e := a.start(ctx, id)
		if e != nil && !hadOld {
			a.DB.Exec(ctx, "UPDATE instances SET status='fault',last_error=$2 WHERE id=$1", id, a.startupError(ctx, id, e))
		}
		return nil, e
	})
}
func (a *App) toolsAPI(w http.ResponseWriter, r *http.Request) {
	if !a.canUseInstance(r.Context(), current(r).ID, r.PathValue("id")) {
		fail(w, 403, "无此 MCP 服务使用权限")
		return
	}
	v, ok := a.Runtime.Load(r.PathValue("id"))
	if !ok {
		respond(w, 200, map[string]any{"items": []any{}, "total": 0})
		return
	}
	s := v.(*snapshot)
	tools := []*mcp.Tool{}
	for _, tool := range s.tools {
		if a.canUseTool(r.Context(), current(r).ID, s.instance.ID, tool.Name) {
			tools = append(tools, tool)
		}
	}
	respond(w, 200, map[string]any{"items": tools, "total": len(tools), "effective_version": s.instance.Version})
}
func (a *App) task(w http.ResponseWriter, r *http.Request, kind string, f func(context.Context) (any, error)) {
	id, e := a.enqueueTask(r.Context(), kind, f)
	if e != nil {
		fail(w, 429, e.Error())
		return
	}
	respond(w, 202, map[string]string{"id": id, "kind": kind, "status": "queued"})
}

func (a *App) enqueueTask(ctx context.Context, kind string, f func(context.Context) (any, error)) (string, error) {
	id := ID()
	_, e := a.DB.Exec(ctx, "INSERT INTO tasks(id,kind,status) VALUES($1,$2,'queued')", id, kind)
	if e != nil {
		return "", errors.New("任务保存失败")
	}
	job := func() {
		slog.Info("后台任务开始", "event", "task_started", "task_id", id, "kind", kind)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		a.DB.Exec(ctx, "UPDATE tasks SET status='running' WHERE id=$1", id)
		result, e := f(ctx)
		status, detail := "succeeded", ""
		if e != nil {
			status = "failed"
			slog.Error("后台任务失败", "event", "task_failed", "task_id", id, "kind", kind, "detail", e.Error())
			detail = "任务失败：请检查配置、连接、凭据和运行依赖"
		}
		if e == nil {
			slog.Info("后台任务完成", "event", "task_completed", "task_id", id, "kind", kind)
		}
		b, _ := json.Marshal(result)
		if b == nil {
			b = []byte("null")
		}
		a.DB.Exec(context.Background(), "UPDATE tasks SET status=$2,detail=$3,result=$4 WHERE id=$1", id, status, detail, b)
	}
	select {
	case a.jobs <- job:
		return id, nil
	default:
		a.DB.Exec(ctx, "UPDATE tasks SET status='failed',detail='任务队列已满' WHERE id=$1", id)
		return id, errors.New("任务队列已满")
	}
}
func (a *App) tasksAPI(w http.ResponseWriter, r *http.Request) {
	if current(r).Role != "admin" {
		fail(w, 403, "任务详情仅管理员可见")
		return
	}
	rows, e := a.DB.Query(r.Context(), "SELECT id,kind,status,detail,result,created_at FROM tasks WHERE ($1='' OR id=$1) ORDER BY created_at DESC LIMIT 100", r.PathValue("id"))
	if e != nil {
		fail(w, 503, "查询失败")
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var id, kind, status, detail string
		var b []byte
		var at string
		rows.Scan(&id, &kind, &status, &detail, &b, &at)
		out = append(out, map[string]any{"id": id, "kind": kind, "status": status, "detail": detail, "result": json.RawMessage(b), "created_at": at})
	}
	if r.PathValue("id") != "" {
		if len(out) == 0 {
			fail(w, 404, "任务不存在")
		} else {
			respond(w, 200, out[0])
		}
		return
	}
	respond(w, 200, map[string]any{"items": out, "total": len(out)})
}
func safePath(roots []string, p string) bool {
	resolved, e := filepath.EvalSymlinks(p)
	if e != nil {
		return false
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		rr, e := filepath.EvalSymlinks(root)
		if e != nil {
			continue
		}
		rel, e := filepath.Rel(rr, resolved)
		if e == nil && rel != ".." && !filepath.IsAbs(rel) && !(len(rel) > 3 && rel[:3] == "../") {
			return true
		}
	}
	return false
}

func (a *App) testInstance(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Instance
		BaseID             string              `json:"base_id"`
		PostgresConnection *postgresConnection `json:"postgres_connection,omitempty"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.BaseID != "" {
		old, e := a.loadInstance(r.Context(), in.BaseID)
		if e != nil {
			fail(w, 404, "基准实例不存在，请刷新")
			return
		}
		in.Instance = mergeRedacted(in.Instance, old)
	}
	if in.PostgresConnection != nil {
		if e := applyPostgresConnection(&in.Instance, *in.PostgresConnection); e != nil {
			fail(w, 400, e.Error())
			return
		}
	}
	if e := validate(&in.Instance); e != nil {
		fail(w, 400, e.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	session, pid, e := a.connectInstance(ctx, in.Instance)
	if e != nil {
		slog.Warn("MCP 连接测试失败", "event", "mcp_test_failed", "instance_name", in.Name, "detail", redactStartupDiagnostic(in.Instance, e.Error()))
		fail(w, 400, "连接失败："+redactStartupDiagnostic(in.Instance, e.Error()))
		return
	}
	defer closeClientSession(session, pid)
	if in.PostgresConnection != nil || isReferencePostgres(in.Instance) {
		if err := verifyPostgres(ctx, session); err != nil {
			slog.Warn("数据库连接测试失败", "event", "mcp_test_failed", "instance_name", in.Name, "detail", redactStartupDiagnostic(in.Instance, err.Error()))
			fail(w, 400, "数据库连接验证失败："+redactStartupDiagnostic(in.Instance, err.Error()))
			return
		}
	}
	tools := []string{}
	for tool, e := range session.Tools(ctx, nil) {
		if e != nil {
			fail(w, 400, "工具发现失败："+e.Error())
			return
		}
		tools = append(tools, tool.Name)
	}
	respond(w, 200, map[string]any{"ok": true, "count": len(tools), "tools": tools})
}

// mergeRedacted restores stored secret values that the API only returns in
// redacted form, using the same semantics as the PATCH handler: a redacted
// value echoed back unchanged means "keep the stored value", and nil
// env/headers means "keep the stored map".
func mergeRedacted(in Instance, old Instance) Instance {
	publicOld := publicInstance(old)
	if reflect.DeepEqual(in.Args, publicOld.Args) {
		in.Args = old.Args
	}
	if in.URL == publicOld.URL {
		in.URL = old.URL
	}
	if in.Env == nil {
		in.Env = old.Env
	}
	if in.Headers == nil {
		in.Headers = old.Headers
	}
	if in.HeaderHelper == "" && in.HeaderHelperConfigured {
		in.HeaderHelper = old.HeaderHelper
	}
	return in
}

var _ = os.Stat

// editorSecrets is explicitly restricted to administrators; public instance APIs
// continue to redact values for all list and read-only views.
func (a *App) editorSecrets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if current(r).Role != "admin" {
		fail(w, 403, "仅管理员可编辑服务配置")
		return
	}
	i, err := a.loadInstance(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 404, "实例不存在")
		return
	}
	respond(w, 200, map[string]any{"version": i.Version, "args": i.Args, "env": i.Env, "headers": i.Headers, "header_helper": i.HeaderHelper})
}
