package platform

// OAuth is an explicitly administrator-initiated browser flow. It never replays
// a failed tools/call, nor interprets a generic web login as MCP authorization.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"toolbox/internal/storage"
)

type mcpOAuthMetadata struct {
	Issuer         string   `json:"issuer"`
	Authorization  string   `json:"authorization_endpoint"`
	Token          string   `json:"token_endpoint"`
	Registration   string   `json:"registration_endpoint"`
	PKCE           []string `json:"code_challenge_methods_supported"`
	AuthMethods    []string `json:"token_endpoint_auth_methods_supported"`
	IssuerResponse bool     `json:"authorization_response_iss_parameter_supported"`
}
type mcpOAuthCredential struct {
	Resource string           `json:"resource"`
	Version  int              `json:"version"`
	Metadata mcpOAuthMetadata `json:"metadata"`
	ClientID string           `json:"client_id"`
	Access   string           `json:"access_token"`
	Refresh  string           `json:"refresh_token"`
	Expires  time.Time        `json:"expires"`
}
type mcpOAuthPending struct {
	Claimed                                                  atomic.Bool
	Canceled                                                 atomic.Bool
	ID, Owner, Resource, Verifier, Callback, Scope, ClientID string
	Version                                                  int
	Metadata                                                 mcpOAuthMetadata
	Expires                                                  time.Time
}

var oauthChallengeParameter = regexp.MustCompile(`(?i)(resource_metadata|scope)\s*=\s*"([^"\r\n]*)"`)

func oauthURL(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("OAuth 地址必须是无凭据的 HTTPS URL")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !publicOAuthIP(ip) {
		return nil, errors.New("OAuth 元数据与认证服务不允许指向本机或内网地址")
	}
	return u, nil
}
func publicOAuthIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}

// Resolve and pin the checked IP for each connection; redirects and proxies are
// disabled so untrusted server metadata cannot access local services via SSRF.
func oauthHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil {
			return nil, errors.New("OAuth 服务域名解析失败")
		}
		if len(ips) == 0 {
			return nil, errors.New("OAuth 服务域名无地址")
		}
		for _, ip := range ips {
			if !publicOAuthIP(ip.IP) {
				return nil, errors.New("OAuth 服务域名解析到了受限地址")
			}
		}
		var last error
		for _, ip := range ips {
			c, e := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if e == nil {
				return c, nil
			}
			last = e
		}
		return nil, last
	}
	return &http.Client{Transport: tr, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("OAuth 服务重定向不受支持") }}
}
func oauthJSON(ctx context.Context, client *http.Client, method, raw string, body any, out any) error {
	if _, e := oauthURL(raw); e != nil {
		return e
	}
	var data io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		data = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, raw, data)
	if e != nil {
		return e
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, e := client.Do(req)
	if e != nil {
		return errors.New("OAuth 服务连接失败，请检查认证服务地址和网络")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("OAuth 服务返回 HTTP %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
}
func discoverMCPOAuth(ctx context.Context, client *http.Client, resource, challenge string) (mcpOAuthMetadata, string, error) {
	var meta mcpOAuthMetadata
	u, e := oauthURL(resource)
	if e != nil {
		return meta, "", e
	}
	if u.RawQuery != "" {
		return meta, "", errors.New("含查询凭据的 MCP URL 不支持浏览器 OAuth，请使用标准无凭据 MCP 地址")
	}
	scope := ""
	paths := []string{}
	for _, p := range oauthChallengeParameter.FindAllStringSubmatch(challenge, -1) {
		if strings.EqualFold(p[1], "resource_metadata") {
			paths = []string{p[2]}
		} else {
			scope = p[2]
		}
	}
	if len(paths) == 0 {
		base := u.Scheme + "://" + u.Host
		paths = append(paths, base+"/.well-known/oauth-protected-resource"+strings.TrimRight(u.EscapedPath(), "/"))
		if u.Path != "" && u.Path != "/" {
			paths = append(paths, base+"/.well-known/oauth-protected-resource")
		}
	}
	var prm struct {
		Resource string   `json:"resource"`
		Issuers  []string `json:"authorization_servers"`
		Scopes   []string `json:"scopes_supported"`
	}
	found := false
	for _, path := range paths {
		if e = oauthJSON(ctx, client, "GET", path, nil, &prm); e == nil {
			found = true
			break
		}
	}
	if !found {
		return meta, "", errors.New("服务未提供可用的标准 MCP OAuth 元数据；请在配置中填写服务要求的请求头或动态口令")
	}
	if prm.Resource != resource || len(prm.Issuers) == 0 {
		return meta, "", errors.New("OAuth 资源元数据与 MCP 地址不匹配，或未声明认证服务器")
	}
	issuer, e := oauthURL(prm.Issuers[0])
	if e != nil {
		return meta, "", e
	}
	if issuer.RawQuery != "" {
		return meta, "", errors.New("OAuth issuer 不允许包含查询参数")
	}
	base := issuer.Scheme + "://" + issuer.Host
	tail := strings.TrimRight(issuer.EscapedPath(), "/")
	candidates := []string{base + "/.well-known/oauth-authorization-server" + tail, base + "/.well-known/openid-configuration" + tail}
	if tail != "" {
		candidates = append(candidates, strings.TrimRight(issuer.String(), "/")+"/.well-known/openid-configuration")
	}
	found = false
	for _, path := range candidates {
		if e = oauthJSON(ctx, client, "GET", path, nil, &meta); e == nil {
			found = true
			break
		}
	}
	if !found || meta.Issuer != prm.Issuers[0] {
		return meta, "", errors.New("OAuth 认证元数据 issuer 校验失败")
	}
	for _, raw := range []string{meta.Authorization, meta.Token} {
		if _, e = oauthURL(raw); e != nil {
			return meta, "", e
		}
	}
	if !slices.Contains(meta.PKCE, "S256") {
		return meta, "", errors.New("认证服务器未声明 PKCE S256，无法安全启动授权")
	}
	if len(meta.AuthMethods) > 0 && !slices.Contains(meta.AuthMethods, "none") {
		return meta, "", errors.New("认证服务器需要机密客户端；当前仅支持公开客户端 PKCE，请联系服务商获取兼容的接入方式")
	}
	// Do not automatically request every advertised scope. An explicit challenge
	// is authoritative; otherwise authorization uses the provider's default scope.
	return meta, scope, nil
}
func (a *App) oauthClient() *http.Client {
	if a.oauthHTTPClient != nil {
		return a.oauthHTTPClient
	}
	return oauthHTTPClient()
}

func (a *App) loadMCPOAuth(ctx context.Context, i Instance) (*mcpOAuthCredential, error) {
	var encoded string
	e := a.DB.QueryRow(ctx, "SELECT value FROM system_settings WHERE key=$1", "mcp_oauth:"+i.ID).Scan(&encoded)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, errors.New("读取 OAuth 凭据失败")
	}
	b, e := base64.StdEncoding.DecodeString(encoded)
	if e != nil {
		return nil, errors.New("OAuth 凭据损坏，请重新授权")
	}
	s, e := a.unseal(b)
	if e != nil {
		return nil, errors.New("OAuth 凭据解密失败，请重新授权")
	}
	var c mcpOAuthCredential
	if len(s.Args) != 1 || json.Unmarshal([]byte(s.Args[0]), &c) != nil {
		return nil, errors.New("OAuth 凭据损坏，请重新授权")
	}
	if c.Resource != i.URL {
		return nil, nil
	}
	a.rememberOAuthSecrets(i.ID, c.Access, c.Refresh)
	return &c, nil
}
func (a *App) saveMCPOAuth(ctx context.Context, id string, c *mcpOAuthCredential) error {
	b, e := json.Marshal(c)
	if e != nil {
		return e
	}
	a.rememberOAuthSecrets(id, c.Access, c.Refresh)
	v := base64.StdEncoding.EncodeToString(a.seal(secrets{Args: []string{string(b)}}))
	result, e := a.DB.Exec(ctx, "INSERT INTO system_settings(key,value) SELECT $1,$2 WHERE EXISTS(SELECT 1 FROM instances WHERE id=$3 AND version=$4) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "mcp_oauth:"+id, v, id, c.Version)
	if e == nil && result.RowsAffected() != 1 {
		return errors.New("MCP 配置已变化，OAuth 凭据未保存")
	}
	return e
}
func oauthExchange(ctx context.Context, client *http.Client, c *mcpOAuthCredential, values url.Values) error {
	if _, e := oauthURL(c.Metadata.Token); e != nil {
		return e
	}
	values.Set("client_id", c.ClientID)
	values.Set("resource", c.Resource)
	req, e := http.NewRequestWithContext(ctx, "POST", c.Metadata.Token, strings.NewReader(values.Encode()))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, e := client.Do(req)
	if e != nil {
		return errors.New("OAuth Token 服务连接失败，请重试授权")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("OAuth Token 交换失败 (HTTP %d)，请重新授权", res.StatusCode)
	}
	var out struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Type    string `json:"token_type"`
		Expires int64  `json:"expires_in"`
	}
	if e = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); e != nil || out.Access == "" || !strings.EqualFold(out.Type, "Bearer") || strings.ContainsAny(out.Access, "\r\n") || out.Expires < 0 || out.Expires > 31536000 {
		return errors.New("OAuth Token 响应无效，请重新授权")
	}
	c.Access = out.Access
	if out.Refresh != "" {
		c.Refresh = out.Refresh
	}
	c.Expires = time.Time{}
	if out.Expires > 0 {
		c.Expires = time.Now().Add(time.Duration(out.Expires) * time.Second)
	}
	return nil
}
func (a *App) oauthInstanceLock(id string) *sync.Mutex {
	v, _ := a.oauthLocks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (a *App) mcpOAuthAccess(ctx context.Context, i Instance) (string, error) {
	lock := a.oauthInstanceLock(i.ID)
	lock.Lock()
	defer lock.Unlock()
	c, e := a.loadMCPOAuth(ctx, i)
	if e != nil || c == nil {
		return "", e
	}
	if !c.Expires.IsZero() && time.Now().Add(30*time.Second).After(c.Expires) {
		if c.Refresh == "" {
			return "", errors.New("MCP 登录已过期，请在实例菜单中重新进行浏览器授权")
		}
		if e = oauthExchange(ctx, a.oauthClient(), c, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.Refresh}}); e != nil {
			return "", e
		}
		c.Version = i.Version
		if e = a.saveMCPOAuth(ctx, i.ID, c); e != nil {
			return "", errors.New("OAuth 刷新结果保存失败")
		}
	}
	return c.Access, nil
}
func (a *App) oauthCallbackURL() (string, error) {
	u, e := url.Parse(a.PublicOrigin())
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("请先配置有效的服务公开访问地址")
	}
	ip := net.ParseIP(u.Hostname())
	loop := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loop && (a.Personal || a.LocalOnly)) {
		return "", errors.New("企业版浏览器 OAuth 需要公开 HTTPS 地址；个人版使用本机 loopback 回调")
	}
	return strings.TrimRight(u.String(), "/") + "/api/v1/mcp-oauth/callback", nil
}
func (a *App) mcpOAuthAPI(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	i, e := a.loadInstance(r.Context(), r.PathValue("id"))
	if e != nil {
		fail(w, 404, "实例不存在")
		return
	}
	if i.Transport != "http" {
		fail(w, 400, "浏览器 OAuth 仅适用于标准远程 HTTP MCP；stdio 请使用环境变量配置凭据")
		return
	}
	if r.Method == "GET" {
		c, e := a.loadMCPOAuth(r.Context(), i)
		if e != nil {
			fail(w, 409, e.Error())
			return
		}
		state := "not_authorized"
		issuer := ""
		if c != nil {
			state = "authorized"
			issuer = c.Metadata.Issuer
			if !c.Expires.IsZero() && time.Now().After(c.Expires) {
				state = "expired"
			}
		}
		if _, challenged := a.oauthChallenges.Load(i.ID); challenged {
			state = "authorization_required"
		}
		pending := false
		a.oauthPending.Range(func(_, v any) bool {
			p := v.(*mcpOAuthPending)
			if p.ID == i.ID && p.Owner == current(r).ID && time.Now().Before(p.Expires) {
				pending = true
			}
			return true
		})
		respond(w, 200, map[string]any{"status": state, "issuer": issuer, "pending": pending})
		return
	}
	// Cancel/revoke locally, including all outstanding callback states. This does
	// not claim to revoke a token at the external provider.
	if r.Method == "DELETE" {
		a.oauthMu.Lock()
		a.oauthPending.Range(func(k, v any) bool {
			if v.(*mcpOAuthPending).ID == i.ID {
				v.(*mcpOAuthPending).Canceled.Store(true)
				a.oauthPending.Delete(k)
			}
			return true
		})
		a.oauthMu.Unlock()
		lock := a.oauthInstanceLock(i.ID)
		lock.Lock()
		_, e = a.DB.Exec(r.Context(), "DELETE FROM system_settings WHERE key=$1", "mcp_oauth:"+i.ID)
		lock.Unlock()
		if e != nil {
			fail(w, 503, "清除 OAuth 凭据失败")
			return
		}
		a.stop(i.ID)
		respond(w, 200, map[string]any{"status": "not_authorized"})
		return
	}
	var input struct {
		ClientID string `json:"client_id"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(input.ClientID) > 2048 || strings.ContainsAny(input.ClientID, "\r\n") {
		fail(w, 400, "Client ID 无效")
		return
	}
	callback, e := a.oauthCallbackURL()
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	challenge := ""
	if v, ok := a.oauthChallenges.Load(i.ID); ok {
		challenge = v.(string)
	}
	meta, scope, e := discoverMCPOAuth(r.Context(), a.oauthClient(), i.URL, challenge)
	if e != nil {
		fail(w, 422, e.Error())
		return
	}
	clientID := strings.TrimSpace(input.ClientID)
	if clientID == "" {
		if meta.Registration == "" {
			fail(w, 422, "服务不支持动态客户端注册，请在认证服务预注册公开客户端，并填写 Client ID；回调地址："+callback)
			return
		}
		var registered struct {
			ID        string   `json:"client_id"`
			Secret    string   `json:"client_secret"`
			Method    string   `json:"token_endpoint_auth_method"`
			Redirects []string `json:"redirect_uris"`
		}
		e = oauthJSON(r.Context(), a.oauthClient(), "POST", meta.Registration, map[string]any{"client_name": "Toolbox", "redirect_uris": []string{callback}, "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}, "token_endpoint_auth_method": "none"}, &registered)
		if e != nil || registered.ID == "" || registered.Secret != "" || (registered.Method != "" && registered.Method != "none") || !slices.Contains(registered.Redirects, callback) {
			fail(w, 422, "OAuth 公开客户端动态注册失败，请预注册 Client ID 后重试")
			return
		}
		clientID = registered.ID
	}
	state, verifier := token(), token()
	sum := sha256.Sum256([]byte(verifier))
	p := &mcpOAuthPending{ID: i.ID, Owner: current(r).ID, Resource: i.URL, Version: i.Version, Verifier: verifier, Callback: callback, Scope: scope, ClientID: clientID, Metadata: meta, Expires: time.Now().Add(10 * time.Minute)}
	a.oauthMu.Lock()
	count := 0
	a.oauthPending.Range(func(k, v any) bool {
		old := v.(*mcpOAuthPending)
		if time.Now().After(old.Expires) || old.ID == i.ID {
			old.Canceled.Store(true)
			a.oauthPending.Delete(k)
		} else {
			count++
		}
		return true
	})
	if count >= 64 {
		a.oauthMu.Unlock()
		fail(w, 429, "待完成授权过多，请稍后重试")
		return
	}
	a.oauthPending.Store(state, p)
	a.oauthMu.Unlock()
	dest, _ := url.Parse(meta.Authorization)
	q := dest.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", callback)
	q.Set("state", state)
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
	q.Set("code_challenge_method", "S256")
	q.Set("resource", i.URL)
	if scope != "" {
		q.Set("scope", scope)
	}
	dest.RawQuery = q.Encode()
	respond(w, 200, map[string]any{"authorization_url": dest.String(), "issuer": meta.Issuer, "expires_at": p.Expires, "callback": callback})
}
func (a *App) mcpOAuthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	state := r.URL.Query().Get("state")
	value, ok := a.oauthPending.Load(state)
	if !ok {
		http.Error(w, "授权状态无效、已使用或已取消，请返回 Toolbox 重试。", 400)
		return
	}
	p := value.(*mcpOAuthPending)
	if !p.Claimed.CompareAndSwap(false, true) || p.Canceled.Load() {
		http.Error(w, "授权状态已使用或已取消。", 400)
		return
	}
	defer a.oauthPending.Delete(state)
	if time.Now().After(p.Expires) {
		http.Error(w, "授权已过期，请返回 Toolbox 重试。", 400)
		return
	}
	if _, e := a.oauthCallbackURL(); e != nil {
		http.Error(w, "服务回调配置已变化，请重新授权。", 400)
		return
	}
	callback, _ := a.oauthCallbackURL()
	if callback != p.Callback {
		http.Error(w, "服务回调地址已变化，请重新授权。", 400)
		return
	}
	i, e := a.loadInstance(r.Context(), p.ID)
	if e != nil || i.URL != p.Resource || i.Version != p.Version {
		http.Error(w, "MCP 配置已变化，请重新授权。", 409)
		return
	}
	if !a.Personal {
		var role string
		var enabled bool
		if e = a.DB.QueryRow(r.Context(), "SELECT role,enabled FROM users WHERE id=$1", p.Owner).Scan(&role, &enabled); e != nil || role != "admin" || !enabled {
			http.Error(w, "发起授权的管理员已失效。", 403)
			return
		}
	}
	issuer := r.URL.Query().Get("iss")
	if (p.Metadata.IssuerResponse && issuer == "") || (issuer != "" && issuer != p.Metadata.Issuer) {
		http.Error(w, "认证服务器身份校验失败。", 400)
		return
	}
	if r.URL.Query().Get("error") != "" {
		http.Error(w, "第三方授权被取消或拒绝，可返回 Toolbox 重新授权。", 400)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 8192 {
		http.Error(w, "缺少有效授权码。", 400)
		return
	}
	c := &mcpOAuthCredential{Resource: p.Resource, Version: p.Version, ClientID: p.ClientID, Metadata: p.Metadata}
	if e = oauthExchange(r.Context(), a.oauthClient(), c, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {p.Callback}, "code_verifier": {p.Verifier}}); e != nil {
		http.Error(w, e.Error(), 502)
		return
	}
	lock := a.oauthInstanceLock(p.ID)
	lock.Lock()
	a.oauthMu.Lock()
	commitCallback, callbackErr := a.oauthCallbackURL()
	if p.Canceled.Load() || time.Now().After(p.Expires) || callbackErr != nil || commitCallback != p.Callback {
		e = errors.New("授权已取消、过期或回调配置已变化")
	} else {
		b, _ := json.Marshal(c)
		encoded := base64.StdEncoding.EncodeToString(a.seal(secrets{Args: []string{string(b)}}))
		result, saveErr := a.DB.Exec(r.Context(), `INSERT INTO system_settings(key,value) SELECT $1,$2 WHERE EXISTS(SELECT 1 FROM instances WHERE id=$3 AND version=$4) AND ($5='' OR EXISTS(SELECT 1 FROM users WHERE id=$5 AND role='admin' AND enabled)) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, "mcp_oauth:"+p.ID, encoded, p.ID, p.Version, func() string {
			if a.Personal {
				return ""
			}
			return p.Owner
		}())
		e = saveErr
		if e == nil && result.RowsAffected() != 1 {
			e = errors.New("实例配置或管理员权限已变化")
		}
	}
	a.oauthMu.Unlock()
	lock.Unlock()
	if e != nil {
		http.Error(w, "授权凭据保存失败，请重试。", 503)
		return
	}
	a.rememberOAuthSecrets(i.ID, c.Access, c.Refresh)
	a.oauthChallenges.Delete(i.ID)
	// Resume using the existing lifecycle endpoint from the authenticated page.
	// Callback success means credentials saved, never a fabricated MCP-ready state.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, "<!doctype html><html lang=zh-CN><meta charset=utf-8><title>Toolbox 授权完成</title><h1>授权凭据已保存</h1><p>请返回 Toolbox 点击“连接服务”验证 MCP。此页面可以关闭。</p></html>")
}

// Independently encrypted OAuth records participate in startup and restore key
// validation, including records whose instance was removed in an older build.
func (a *App) verifyMCPOAuthSecrets(ctx context.Context, db *storage.DB) error {
	rows, e := db.Query(ctx, "SELECT value FROM system_settings WHERE key LIKE 'mcp_oauth:%'")
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var encoded string
		if e = rows.Scan(&encoded); e != nil {
			return e
		}
		b, e := base64.StdEncoding.DecodeString(encoded)
		if e != nil {
			return errors.New("OAuth 加密凭据格式无效")
		}
		s, e := a.unseal(b)
		if e != nil {
			return errors.New("MASTER_KEY cannot decrypt OAuth credentials")
		}
		var c mcpOAuthCredential
		if len(s.Args) != 1 || json.Unmarshal([]byte(s.Args[0]), &c) != nil {
			return errors.New("OAuth 加密凭据格式无效")
		}
	}
	return rows.Err()
}
func (a *App) invalidateMCPOAuthPending(id string) {
	a.oauthMu.Lock()
	defer a.oauthMu.Unlock()
	a.oauthPending.Range(func(k, v any) bool {
		p := v.(*mcpOAuthPending)
		if p.ID == id {
			p.Canceled.Store(true)
			a.oauthPending.Delete(k)
		}
		return true
	})
	a.oauthChallenges.Delete(id)
}

// Keep a bounded history of emitted credentials, so an in-flight error can be
// redacted even after refresh-token rotation. No value is serialized or logged.
type oauthSecretHistory struct {
	mu     sync.Mutex
	values []string
}

func (a *App) rememberOAuthSecrets(id string, values ...string) {
	value, _ := a.oauthSecrets.LoadOrStore(id, &oauthSecretHistory{})
	h := value.(*oauthSecretHistory)
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, secret := range values {
		if secret != "" && !slices.Contains(h.values, secret) {
			h.values = append(h.values, secret)
		}
	}
	if len(h.values) > 128 {
		h.values = append([]string{}, h.values[len(h.values)-128:]...)
	}
}
func (a *App) oauthDiagnosticValues(id string) []string {
	value, ok := a.oauthSecrets.Load(id)
	if !ok {
		return nil
	}
	h := value.(*oauthSecretHistory)
	h.mu.Lock()
	defer h.mu.Unlock()
	out := append([]string{}, h.values...)
	for _, secret := range h.values {
		out = append(out, url.QueryEscape(secret))
	}
	return out
}
