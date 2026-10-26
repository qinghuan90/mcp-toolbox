package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type snapshot struct {
	healthMu     sync.RWMutex
	health       *InstanceHealth
	sessionEnded atomic.Bool
	generation   string
	instance     Instance
	session      *mcp.ClientSession
	tools        []*mcp.Tool
	slots        chan struct{}
	mu           sync.RWMutex
	closed       bool
	pid          int
	startedAt    time.Time
	restartCount int
}
type credential struct{ UserID, TokenID, Client string }
type gatewayLease struct {
	mu       sync.Mutex
	tokenID  string
	sessions map[string]*mcp.ClientSession
	pids     map[string]int
	started  map[string]time.Time
	closed   bool
	timer    *time.Timer
}
type headerTransport struct {
	oauthApp      *App
	oauthInstance Instance
	headers       map[string]string
	helper        string
	allowHelper   *atomic.Bool
	base          http.RoundTripper
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	if t.helper != "" {
		if t.allowHelper == nil || !t.allowHelper.Load() {
			return nil, errors.New("dynamic header helper disabled; enable local command execution in system settings")
		}
		headers, err := runHeaderHelper(r.Context(), t.helper)
		if err != nil {
			return nil, err
		}
		for k, v := range headers {
			r.Header.Set(k, v)
		}
	}
	if t.oauthApp != nil {
		access, err := t.oauthApp.mcpOAuthAccess(r.Context(), t.oauthInstance)
		if err != nil {
			return nil, err
		}
		if access != "" {
			if r.Header.Get("Authorization") != "" {
				return nil, errors.New("OAuth 与自定义 Authorization 请求头冲突，请保留一种认证方式")
			}
			r.Header.Set("Authorization", "Bearer "+access)
		}
	}
	response, err := t.base.RoundTrip(r)
	if err == nil && response.StatusCode == http.StatusUnauthorized && t.oauthApp != nil {
		challenge := strings.Join(response.Header.Values("WWW-Authenticate"), ", ")
		if t.oauthInstance.ID != "" && len(challenge) <= 8192 {
			t.oauthApp.oauthChallenges.Store(t.oauthInstance.ID, challenge)
		}
		// Never replay an application request after authorization failures.
		response.Body.Close()
		return nil, errors.New("服务要求登录或认证。请保存实例后点击“浏览器授权”；若服务不支持标准 OAuth，请在配置中填写 Token/请求头")
	}
	if err == nil && response.StatusCode >= 200 && response.StatusCode < 300 && t.oauthApp != nil {
		t.oauthApp.oauthChallenges.Delete(t.oauthInstance.ID)
	}
	return response, err
}
func (a *App) start(ctx context.Context, id string) (result error) {
	name := ""
	defer func() {
		if result != nil {
			slog.Warn("MCP 启动失败", "event", "mcp_start_failed", "instance_id", id, "instance_name", name, "detail", a.startupError(context.WithoutCancel(ctx), id, result))
		} else {
			slog.Info("MCP 已连接", "event", "mcp_connected", "instance_id", id, "instance_name", name)
		}
	}()
	i, e := a.loadInstance(ctx, id)
	if e != nil {
		return e
	}
	name = i.Name
	// The reference PostgreSQL server only opens its database on the first
	// query. A successful MCP handshake alone cannot establish availability.
	if isReferencePostgres(i) {
		checkCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		return a.startPostgres(checkCtx, id)
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
	session, pid, e := a.connectInstance(ctx, i)
	if e != nil {
		return e
	}
	tools := []*mcp.Tool{}
	connectCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for tool, e := range session.Tools(connectCtx, nil) {
		if e != nil {
			closeClientSession(session, pid)
			return e
		}
		tools = append(tools, tool)
	}
	h.connected(i, len(tools))
	s := &snapshot{health: h, generation: ID(), instance: i, session: session, tools: tools, slots: make(chan struct{}, i.Concurrency), startedAt: time.Now(), pid: pid}
	if prior, ok := a.Runtime.Load(id); ok {
		s.restartCount = prior.(*snapshot).restartCount + 1
	}
	if e = a.publishSnapshot(id, s, func() error {
		_, err := a.DB.Exec(ctx, "UPDATE instances SET status='ready',last_error='',runtime_generation=$2 WHERE id=$1", id, s.generation)
		return err
	}); e != nil {
		closeClientSession(session, pid)
		return e
	}
	a.closeInstanceLeases(id)
	return nil
}

func (a *App) connectInstance(ctx context.Context, i Instance) (*mcp.ClientSession, int, error) {
	var t mcp.Transport
	var pid int
	var diagnostic startupDiagnostic
	if i.Transport == "stdio" {
		if !a.AllowStdio.Load() {
			return nil, 0, errors.New("本机 stdio 已关闭，请在“系统设置 → 本机运行环境”启用后重试")
		}
		cmd := exec.Command(i.Command, i.Args...)
		cmd.Stderr = &diagnostic
		if err := isolateProcess(cmd); err != nil {
			return nil, 0, err
		}
		cmd.Dir = i.Cwd
		cmd.Env = platformProcessEnv(false)
		for k, v := range i.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		t = &mcp.CommandTransport{Command: cmd, TerminateDuration: time.Second}
	} else {
		t = &mcp.StreamableClientTransport{Endpoint: i.URL, MaxRetries: -1, DisableStandaloneSSE: true, HTTPClient: &http.Client{Transport: headerTransport{oauthApp: a, oauthInstance: i, headers: i.Headers, helper: i.HeaderHelper, allowHelper: &a.AllowStdio, base: http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect disabled") }}}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "toolbox", Version: "0.1.0"}, nil)
	connectCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	session, e := client.Connect(connectCtx, t, nil)
	if e != nil {
		if command, ok := t.(*mcp.CommandTransport); ok && command.Command.Process != nil {
			terminateProcessGroup(command.Command.Process.Pid)
		}
		if i.Transport == "stdio" && diagnostic.String() != "" && !errors.Is(e, context.DeadlineExceeded) {
			return nil, 0, errors.New(redactStartupDiagnostic(i, e.Error()+"；进程诊断："+diagnostic.String()))
		}
		return nil, 0, e
	}
	if i.Transport == "stdio" {
		if command, ok := t.(*mcp.CommandTransport); ok && command.Command.Process != nil {
			pid = command.Command.Process.Pid
			if err := registerProcessGroup(pid); err != nil {
				terminateProcessGroup(pid)
				_ = session.Close()
				return nil, 0, errors.New("无法建立本机 MCP 进程生命周期隔离")
			}
		}
	}
	return session, pid, nil
}

func (a *App) publishSnapshot(id string, candidate *snapshot, commit func() error) error {
	// Lifecycle owns the instance lock; drain requests before committing the new
	// generation. A failed commit leaves the old connection open and published.
	var old *snapshot
	if value, loaded := a.Runtime.Load(id); loaded {
		old = value.(*snapshot)
		old.mu.Lock()
		defer old.mu.Unlock()
	}
	if err := commit(); err != nil {
		return err
	}
	if old != nil {
		old.closed = true
		closeClientSession(old.session, old.pid)
	}
	a.Runtime.Store(id, candidate)
	a.instanceHealth.Delete(id)
	if candidate.session != nil {
		go func() { _ = candidate.session.Wait(); candidate.sessionEnded.Store(true) }()
	}
	return nil
}

func closeClientSession(session *mcp.ClientSession, pid int) {
	if session != nil {
		session.Close()
	}
	terminateProcessGroup(pid)
}
func closeSnapshot(s *snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.sessionEnded.Store(true)
	closeClientSession(s.session, s.pid)
}
func (a *App) stop(id string) {
	if v, ok := a.Runtime.LoadAndDelete(id); ok {
		closeSnapshot(v.(*snapshot))
	}
	a.instanceHealth.Delete(id)
	a.closeInstanceLeases(id)
}
func (a *App) Authenticate(ctx context.Context, bearer string) (credential, error) {
	var c credential
	e := a.DB.QueryRow(ctx, "SELECT t.user_id,t.id,t.client_name FROM tokens t JOIN users u ON u.id=t.user_id WHERE t.hash=$1 AND NOT t.revoked AND NOT t.client_deleted AND (t.never_expires OR julianday(t.expires_at)>julianday('now')) AND u.enabled AND NOT u.must_change_password", hash(bearer)).Scan(&c.UserID, &c.TokenID, &c.Client)
	return c, e
}
func (a *App) allowed(ctx context.Context, c credential, id, tool string) bool {
	var yes bool
	e := a.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tokens t JOIN users u ON u.id=t.user_id WHERE t.id=$1 AND t.user_id=$4 AND NOT t.revoked AND NOT t.client_deleted AND (t.never_expires OR julianday(t.expires_at)>julianday('now')) AND u.enabled AND NOT u.must_change_password AND `+toolRolePermission+`) AND EXISTS(SELECT 1 FROM instances WHERE id=$2 AND enabled AND status='ready')`, c.TokenID, id, tool, c.UserID).Scan(&yes)
	return e == nil && yes
}
func routeName(id, tool string) string { return "m_" + id + "_" + hash(tool)[:16] }
func (a *App) sourceIPAllowed(ctx context.Context, remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	rows, err := a.DB.Query(ctx, "SELECT cidr,effect FROM ip_rules")
	if err != nil {
		return false
	}
	defer rows.Close()
	hasAllow, matchedAllow, matchedDeny := false, false, false
	for rows.Next() {
		var cidr, effect string
		if rows.Scan(&cidr, &effect) != nil {
			return false
		}
		prefix, parseErr := netip.ParsePrefix(cidr)
		if parseErr != nil {
			return false
		}
		if effect == "allow" {
			hasAllow = true
		}
		if prefix.Contains(ip) {
			matchedAllow = matchedAllow || effect == "allow"
			matchedDeny = matchedDeny || effect == "deny"
		}
	}
	return rows.Err() == nil && !matchedDeny && (!hasAllow || matchedAllow)
}
func (a *App) Gateway(w http.ResponseWriter, r *http.Request) {
	if !a.originOK(r) {
		fail(w, 403, "请求来源不允许")
		return
	}
	if !a.sourceIPAllowed(r.Context(), r.RemoteAddr) {
		fail(w, 403, "客户端来源 IP 不在调用名单允许范围")
		return
	}
	if r.Method == http.MethodGet {
		ctx, cancel := context.WithCancel(r.Context())
		streamID := ID()
		a.gatewayMu.Lock()
		if a.restoring.Load() {
			a.gatewayMu.Unlock()
			cancel()
			fail(w, 503, "恢复维护中")
			return
		}
		a.gatewayStreams[streamID] = cancel
		a.gatewayMu.Unlock()
		defer func() { a.gatewayMu.Lock(); delete(a.gatewayStreams, streamID); a.gatewayMu.Unlock(); cancel() }()
		r = r.WithContext(ctx)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	a.gatewayMu.Lock()
	if a.gatewayHandler == nil {
		a.gatewayHandler = a.newGatewayHandler()
	}
	handler := a.gatewayHandler
	a.gatewayMu.Unlock()
	if r.Method == http.MethodDelete {
		capture := &deleteStatusWriter{ResponseWriter: w}
		handler.ServeHTTP(capture, r)
		if capture.status != http.StatusNoContent {
			return
		}
		id := r.Header.Get("Mcp-Session-Id")
		a.gatewayMu.Lock()
		lease := a.gatewayLeases[id]
		if lease != nil {
			delete(a.gatewayLeases, id)
		}
		a.gatewayMu.Unlock()
		if lease != nil {
			lease.close()
		}
		return
	}
	handler.ServeHTTP(w, r)
}

type deleteStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *deleteStatusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *deleteStatusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.ResponseWriter.Write(b)
}

func (a *App) newGatewayHandler() http.Handler {
	stream := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		c, e := a.Authenticate(r.Context(), bearer)
		if e != nil {
			return nil
		}
		lease := &gatewayLease{tokenID: c.TokenID, sessions: map[string]*mcp.ClientSession{}, pids: map[string]int{}, started: map[string]time.Time{}}
		var sessionID string
		s := mcp.NewServer(&mcp.Implementation{Name: "Toolbox Gateway", Version: "0.1.0"}, &mcp.ServerOptions{GetSessionID: func() string {
			sessionID = ID()
			a.gatewayMu.Lock()
			a.gatewayLeases[sessionID] = lease
			a.gatewayMu.Unlock()
			lease.mu.Lock()
			lease.timer = time.AfterFunc(16*time.Minute, func() { a.removeGatewayLease(sessionID, lease) })
			lease.mu.Unlock()
			return sessionID
		}})
		catalog := &gatewayCatalog{app: a, server: s, credential: c, lease: lease}
		catalog.refresh()
		s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				var routes map[string]gatewayRoute
				if method == "tools/list" || method == "tools/call" {
					routes = catalog.refresh()
				}
				if method == "tools/call" {
					call, ok := req.(*mcp.CallToolRequest)
					registered := false
					if ok {
						_, registered = routes[call.Params.Name]
					}
					if ok && !registered {
						_, err := a.DB.Exec(ctx, "INSERT INTO logs(id,user_id,token_id,client_name,tool_name,status,detail) VALUES($1,$2,$3,$4,$5,'rejected','unknown or forbidden tool')", ID(), c.UserID, c.TokenID, c.Client, call.Params.Name)
						if err != nil {
							return nil, errors.New("audit unavailable")
						}
					}
				}
				result, err := next(ctx, method, req)
				if err == nil && method == "tools/list" {
					if listed, ok := result.(*mcp.ListToolsResult); ok {
						filtered := make([]*mcp.Tool, 0, len(listed.Tools))
						for _, tool := range listed.Tools {
							route, exists := routes[tool.Name]
							if exists && a.allowed(ctx, c, route.snapshot.instance.ID, route.original) {
								filtered = append(filtered, tool)
							}
						}
						copyResult := *listed
						copyResult.Tools = filtered
						return &copyResult, nil
					}
				}
				return result, err
			}
		})
		return s
	}, &mcp.StreamableHTTPOptions{JSONResponse: true, SessionTimeout: 15 * time.Minute})
	return auth.RequireBearerToken(func(ctx context.Context, bearer string, r *http.Request) (*auth.TokenInfo, error) {
		if r.Method == http.MethodGet {
			a.maintenance.RLock()
			defer a.maintenance.RUnlock()
			if a.restoring.Load() {
				return nil, auth.ErrInvalidToken
			}
		}
		c, e := a.Authenticate(ctx, bearer)
		if e != nil {
			return nil, auth.ErrInvalidToken
		}
		if id := r.Header.Get("Mcp-Session-Id"); id != "" {
			a.gatewayMu.Lock()
			lease := a.gatewayLeases[id]
			a.gatewayMu.Unlock()
			if lease != nil && lease.tokenID == c.TokenID {
				lease.mu.Lock()
				if lease.timer != nil {
					lease.timer.Reset(16 * time.Minute)
				}
				lease.mu.Unlock()
			}
		}
		if _, e = a.DB.Exec(ctx, "UPDATE tokens SET last_seen_at=CURRENT_TIMESTAMP WHERE id=$1", c.TokenID); e != nil {
			return nil, e
		}
		return &auth.TokenInfo{UserID: c.TokenID, Expiration: time.Now().Add(time.Minute)}, nil
	}, nil)(stream)
}
func (a *App) removeGatewayLease(id string, lease *gatewayLease) {
	a.gatewayMu.Lock()
	if a.gatewayLeases[id] == lease {
		delete(a.gatewayLeases, id)
	}
	a.gatewayMu.Unlock()
	lease.close()
}
func (lease *gatewayLease) close() {
	lease.mu.Lock()
	if lease.closed {
		lease.mu.Unlock()
		return
	}
	lease.closed = true
	if lease.timer != nil {
		lease.timer.Stop()
	}
	sessions := lease.sessions
	pids := lease.pids
	lease.sessions = map[string]*mcp.ClientSession{}
	lease.pids = map[string]int{}
	lease.started = map[string]time.Time{}
	lease.mu.Unlock()
	for id, session := range sessions {
		closeClientSession(session, pids[id])
	}
}
func (lease *gatewayLease) isClosed() bool {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return lease.closed
}
func (a *App) closeGatewayLeases() {
	a.gatewayMu.Lock()
	leases := a.gatewayLeases
	streams := a.gatewayStreams
	a.gatewayStreams = map[string]context.CancelFunc{}
	a.gatewayLeases = map[string]*gatewayLease{}
	a.gatewayHandler = nil
	a.gatewayMu.Unlock()
	for _, cancel := range streams {
		cancel()
	}
	for _, lease := range leases {
		lease.close()
	}
}
func (a *App) closeInstanceLeases(id string) {
	a.gatewayMu.Lock()
	leases := make([]*gatewayLease, 0, len(a.gatewayLeases))
	for _, lease := range a.gatewayLeases {
		leases = append(leases, lease)
	}
	a.gatewayMu.Unlock()
	for _, lease := range leases {
		lease.mu.Lock()
		session := lease.sessions[id]
		pid := lease.pids[id]
		delete(lease.sessions, id)
		delete(lease.pids, id)
		delete(lease.started, id)
		lease.mu.Unlock()
		if session != nil {
			closeClientSession(session, pid)
		}
	}
}
func (a *App) call(ctx context.Context, c credential, snap *snapshot, lease *gatewayLease, tool string, args json.RawMessage) (*mcp.CallToolResult, error) {
	if a.auditBlocked.Load() {
		return nil, errors.New("audit recovery in progress")
	}
	id := snap.instance.ID
	registered := snap
	// Resolve at dispatch time: long-lived Gateway sessions must not pin an
	// upstream connection generation after health recovery or an explicit restart.
	if current, ok := a.Runtime.Load(id); ok {
		snap = current.(*snapshot)
	}
	requestID := ID()
	start := time.Now()
	status := "rejected"
	detail := "permission denied"
	_, e := a.DB.Exec(ctx, "INSERT INTO logs(id,user_id,token_id,client_name,instance_id,tool_name,status,runtime_generation) VALUES($1,$2,$3,$4,$5,$6,'accepted',$7)", requestID, c.UserID, c.TokenID, c.Client, id, tool, snap.generation)
	if e != nil {
		return nil, errors.New("audit unavailable")
	}
	defer func() {
		if status == "failed" || status == "unknown" {
			slog.Error("MCP 工具调用异常", "event", "mcp_call_failed", "request_id", requestID, "instance_id", id, "instance_name", snap.instance.Name, "tool", tool, "status", status, "detail", detail)
		}
		auditCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := a.DB.Exec(auditCtx, "UPDATE logs SET status=$2,detail=$3,duration_ms=$4 WHERE id=$1", requestID, status, detail, time.Since(start).Milliseconds()); err != nil {
			a.auditOutcome(requestID, status, detail, time.Since(start).Milliseconds())
			slog.Error("call outcome queued for recovery", "request_id", requestID)
		}
	}()
	v, ok := a.Runtime.Load(id)
	if !ok || v != snap {
		status, detail = "failed", "instance connection unavailable before dispatch"
		return nil, fmt.Errorf("实例暂不可用，尚未发送工具调用；请等待连接恢复或启动实例后再试 [%s]", requestID)
	}
	if !sameGatewayTool(registered, snap, tool) {
		status, detail = "failed", "upstream tool removed or definition changed"
		return nil, fmt.Errorf("工具已移除或定义已更新，请刷新工具列表 [%s]", requestID)
	}
	if !a.allowed(ctx, c, id, tool) {
		return nil, fmt.Errorf("未获此 MCP 或工具使用权限，或 Token 已失效、实例已停用 [%s]", requestID)
	}
	u, _ := a.users.LoadOrStore(c.UserID, make(chan struct{}, 5))
	user := u.(chan struct{})
	acquire := func(ch chan struct{}) bool {
		select {
		case ch <- struct{}{}:
			return true
		default:
			return false
		}
	}
	if !acquire(snap.slots) {
		detail = "instance busy"
		status = "limited"
		return nil, errors.New(detail)
	}
	defer func() { <-snap.slots }()
	if !acquire(user) {
		detail = "user busy"
		status = "limited"
		return nil, errors.New(detail)
	}
	defer func() { <-user }()
	if !acquire(a.global) {
		detail = "gateway busy"
		status = "limited"
		return nil, errors.New(detail)
	}
	defer func() { <-a.global }()
	snap.mu.RLock()
	defer snap.mu.RUnlock()
	if snap.closed || !a.allowed(ctx, c, id, tool) {
		status, detail = "failed", "instance or permission changed"
		return nil, errors.New("instance or permission changed")
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(snap.instance.Timeout)*time.Second)
	defer cancel()
	var downstream *mcp.ClientSession
	if snap.instance.Transport == "stdio" {
		lease.mu.Lock()
		if lease.closed {
			lease.mu.Unlock()
			status = "failed"
			detail = "upstream session closed"
			return nil, errors.New(detail)
		}
		downstream = lease.sessions[id]
		if downstream == nil {
			var pid int
			downstream, pid, e = a.connectInstance(callCtx, snap.instance)
			if e == nil {
				lease.sessions[id] = downstream
				lease.pids[id] = pid
				lease.started[id] = time.Now()
			}
		}
		lease.mu.Unlock()
		if e != nil {
			status = "failed"
			detail = "downstream connection failed"
			return nil, fmt.Errorf("%s [%s]", detail, requestID)
		}
	} else {
		downstream = snap.session
	}
	if !a.allowed(callCtx, c, id, tool) {
		detail = "permission changed"
		return nil, errors.New("MCP 服务使用权限已被撤销")
	}
	result, e := downstream.CallTool(callCtx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if e != nil {
		if reported, code := reportedToolFailure(e, snap.instance, requestID); reported != nil {
			status, detail = "failed", fmt.Sprintf("downstream JSON-RPC error %d", code)
			return reported, nil
		}
		status = "unknown"
		detail = "execution outcome unknown"
		a.Runtime.CompareAndDelete(id, snap)
		// A transport error cannot prove that an accepted request did not run.
		quarantineCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		_, quarantineErr := a.DB.Exec(quarantineCtx, quarantineSQL, id, snap.generation)
		stop()
		if quarantineErr != nil {
			slog.Error("quarantine pending recovery", "instance_id", id)
		}
		go closeSnapshot(snap)
		go a.closeInstanceLeases(id)
		return nil, fmt.Errorf("%s [%s]", detail, requestID)
	}
	b, _ := json.Marshal(result)
	if len(b) > 4<<20 {
		status = "failed"
		detail = "result exceeds 4 MiB"
		return nil, errors.New(detail)
	}
	status = "succeeded"
	detail = ""
	if result.IsError {
		status = "failed"
		detail = "downstream tool error"
	} else {
		snap.healthUpdate(func(h *InstanceHealth) { now := time.Now().UTC(); h.LastTool = tool; h.LastToolAt = &now })
	}
	return result, nil
}

var errorURLPattern = regexp.MustCompile(`(?i)(?:postgres(?:ql)?|https?)://[^\s"'<>]+`)

// A structured JSON-RPC response is a reported downstream failure, not a lost
// transport outcome. Keep the connection usable and never retry the call.
func reportedToolFailure(err error, instance Instance, requestID string) (*mcp.CallToolResult, int64) {
	var remote *jsonrpc.Error
	if !errors.As(err, &remote) {
		return nil, 0
	}
	message := remote.Message
	for _, value := range instanceDiagnosticValues(instance) {
		if len(value) >= 4 {
			message = strings.ReplaceAll(message, value, "<REDACTED>")
		}
		if parsed, e := url.Parse(value); e == nil && parsed.User != nil {
			if password, ok := parsed.User.Password(); ok && password != "" {
				message = strings.ReplaceAll(message, password, "<REDACTED>")
			}
		}
	}
	for _, values := range []map[string]string{instance.Env, instance.Headers} {
		for _, value := range values {
			if value != "" {
				message = strings.ReplaceAll(message, value, "<REDACTED>")
			}
		}
	}
	message = errorURLPattern.ReplaceAllString(message, "<REDACTED_URL>")
	if text := []rune(message); len(text) > 1200 {
		message = string(text[:1200]) + "…"
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("下游工具执行失败（JSON-RPC %d）：%s [%s]", remote.Code, message, requestID)}}}, remote.Code
}

const quarantineSQL = "UPDATE instances SET status='quarantined',last_error='调用超时，结果未知；请人工确认后重启' WHERE id=$1 AND runtime_generation=$2"
const restoreQuarantineSQL = "UPDATE instances SET status='quarantined',last_error='存在执行结果未知的调用，请人工确认后重启' WHERE EXISTS (SELECT 1 FROM logs l WHERE l.instance_id=instances.id AND l.runtime_generation=instances.runtime_generation AND l.status IN ('accepted','unknown'))"

func (a *App) Restore(ctx context.Context) error {
	if err := a.reloadHealthSettings(ctx); err != nil {
		return err
	}
	if err := a.reloadScanRoots(ctx); err != nil {
		return err
	}
	if err := a.reloadStdioSetting(ctx); err != nil {
		return err
	}
	// Never reconnect before recovering uncertainty from durable call admission.
	// Failure is closed: no instances are restored when this check cannot commit.
	if _, e := a.DB.Exec(ctx, restoreQuarantineSQL); e != nil {
		slog.Error("runtime recovery blocked: cannot persist uncertain calls")
		return e
	}
	if _, e := a.DB.Exec(ctx, "UPDATE tasks SET status='failed',detail='服务重启，任务未完成，请检查后重试' WHERE status IN ('queued','running')"); e != nil {
		return e
	}
	if _, e := a.DB.Exec(ctx, "UPDATE logs SET status='unknown',detail='服务重启，执行结果未知' WHERE status='accepted'"); e != nil {
		return e
	}
	rows, e := a.DB.Query(ctx, "SELECT id FROM instances WHERE enabled AND status!='quarantined'")
	if e != nil {
		return e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e := rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	if e := rows.Err(); e != nil {
		rows.Close()
		return e
	}
	rows.Close()
	// Connection failures are instance faults, not a reason to discard valid
	// restored data. Reconnect in parallel so many unavailable servers do not
	// serialize the complete recovery for several minutes.
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	critical := make(chan error, len(ids))
	for _, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			if e := a.start(ctx, id); e != nil {
				writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if _, updateErr := a.DB.Exec(writeCtx, "UPDATE instances SET status='fault',last_error=$2 WHERE id=$1", id, a.startupError(writeCtx, id, e)); updateErr != nil {
					critical <- updateErr
				}
			}
		}(id)
	}
	wg.Wait()
	close(critical)
	var failures []error
	for e := range critical {
		failures = append(failures, e)
	}
	if err := ctx.Err(); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}
func (a *App) Close() {
	if a.healthCancel != nil {
		a.healthCancel()
		a.healthWG.Wait()
	}
	a.closeRuntime()
}

func (a *App) closeRuntime() {
	a.closeGatewayLeases()
	a.Runtime.Range(func(k, v any) bool { a.stop(k.(string)); return true })
}
