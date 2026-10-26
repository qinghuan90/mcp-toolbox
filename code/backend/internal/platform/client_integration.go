package platform

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

var clientIntegrationMu sync.Mutex

const integrationPrefix = "client_integration:"

type integrationRequest struct {
	GatewayURL   string `json:"gateway_url"`
	Client       string `json:"client"`
	Directory    string `json:"directory"`
	Name         string `json:"server_name"`
	UserID       string `json:"user_id"`
	Days         int    `json:"days"`
	NeverExpires bool   `json:"never_expires"`
	Fingerprint  string `json:"fingerprint"`
	Path         string `json:"path"`
	Replace      bool   `json:"replace"`
	RequestID    string `json:"request_id"`
}
type clientIntegration struct {
	ID           string         `json:"id"`
	Client       string         `json:"client"`
	ClientName   string         `json:"client_name"`
	Path         string         `json:"path"`
	Format       string         `json:"format"`
	Name         string         `json:"server_name"`
	UserID       string         `json:"user_id"`
	TokenID      string         `json:"token_id"`
	Backup       string         `json:"backup_path"`
	Before       string         `json:"before_fingerprint"`
	Previous     map[string]any `json:"previous"`
	Applied      map[string]any `json:"applied"`
	State        string         `json:"state"`
	Created      time.Time      `json:"created_at"`
	Expires      time.Time      `json:"expires_at"`
	NeverExpires bool           `json:"never_expires"`
}

func (i clientIntegration) public() map[string]any {
	return map[string]any{"id": i.ID, "client": i.Client, "client_name": i.ClientName, "path": i.Path, "server_name": i.Name, "user_id": i.UserID, "token_id": i.TokenID, "backup_path": i.Backup, "state": i.State, "created_at": i.Created, "expires_at": i.Expires, "never_expires": i.NeverExpires}
}
func (a *App) encodeIntegration(i clientIntegration) (string, error) {
	raw, err := json.Marshal(i)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.Key.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(a.Key.Seal(nonce, nonce, raw, nil)), nil
}
func (a *App) decodeIntegration(value string) (clientIntegration, error) {
	var i clientIntegration
	b, err := base64.StdEncoding.DecodeString(value)
	n := a.Key.NonceSize()
	if err != nil || len(b) < n {
		return i, errors.New("接入记录不可读")
	}
	raw, err := a.Key.Open(nil, b[:n], b[n:], nil)
	if err != nil {
		return i, errors.New("接入记录密钥不匹配")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	err = dec.Decode(&i)
	if i.Format == "toml" {
		i.Previous = normalizeClientNumbers(i.Previous)
		i.Applied = normalizeClientNumbers(i.Applied)
	}
	return i, err
}
func (a *App) readIntegration(ctx context.Context, id string) (clientIntegration, error) {
	var value string
	err := a.DB.QueryRow(ctx, "SELECT value FROM system_settings WHERE key=$1", integrationPrefix+id).Scan(&value)
	if err != nil {
		return clientIntegration{}, err
	}
	return a.decodeIntegration(value)
}
func (a *App) saveIntegration(ctx context.Context, i clientIntegration) error {
	value, err := a.encodeIntegration(i)
	if err != nil {
		return err
	}
	_, err = a.DB.Exec(ctx, "INSERT INTO system_settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=excluded.value", integrationPrefix+i.ID, value)
	return err
}
func (a *App) clientIntegrationTargets(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	targets := []map[string]any{}
	for _, t := range clientTargets() {
		_, path, err := resolveClientTarget(t.ID, t.Directory)
		exists := false
		if err == nil {
			_, exists, err = readClientFile(path)
		}
		targets = append(targets, map[string]any{"id": t.ID, "name": t.Name, "directory": t.Directory, "relative_file": t.Relative, "path": path, "exists": exists, "readable": err == nil})
	}
	respond(w, 200, map[string]any{"items": targets, "gateway_url": a.PublicOrigin() + "/mcp", "location": "Toolbox 服务所在电脑"})
}
func (a *App) validateIntegrationRequest(r *http.Request, in *integrationRequest) (clientTarget, string, []byte, bool, error) {
	if in.Name == "" {
		in.Name = "toolbox"
	}
	if !clientNamePattern.MatchString(in.Name) {
		return clientTarget{}, "", nil, false, errors.New("服务名称仅支持字母、数字、下划线或短横线，最多64位")
	}
	if in.Days == 0 {
		in.Days = 30
	}
	if !in.NeverExpires && (in.Days < 1 || in.Days > 365) {
		return clientTarget{}, "", nil, false, errors.New("Token 有效期须为1–365天")
	}
	if a.Personal || in.UserID == "" {
		in.UserID = current(r).ID
	}
	var enabled bool
	if err := a.DB.QueryRow(r.Context(), "SELECT enabled FROM users WHERE id=$1", in.UserID).Scan(&enabled); err != nil || !enabled {
		return clientTarget{}, "", nil, false, errors.New("所属用户不存在或已停用")
	}
	t, path, err := resolveClientTarget(in.Client, in.Directory)
	if err != nil {
		return t, path, nil, false, err
	}
	raw, exists, err := readClientFile(path)
	return t, path, raw, exists, err
}
func (a *App) previewClientIntegration(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var in integrationRequest
	if !decode(w, r, &in) {
		return
	}
	t, path, raw, exists, err := a.validateIntegrationRequest(r, &in)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	proposed := clientEntry(t.Format, a.PublicOrigin()+"/mcp", "<TOKEN 自动生成，保存到客户端配置>")
	_, old, err := editClientConfig(raw, t.Format, in.Name, proposed)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	root, _ := decodeClientConfig(raw, t.Format)
	block, _ := json.MarshalIndent(proposed, "", "  ")
	if t.Format == "toml" {
		block = []byte(fmt.Sprintf("[mcp_servers.%s]\nurl = %q\nenabled = true\n[mcp_servers.%s.http_headers]\nAuthorization = \"Bearer <自动生成>\"\n", in.Name, a.PublicOrigin()+"/mcp", in.Name))
	}
	respond(w, 200, map[string]any{"client": t.ID, "client_name": t.Name, "directory": in.Directory, "server_name": in.Name, "path": path, "fingerprint": fileFingerprint(raw, exists), "exists": exists, "conflict": old != nil, "existing_servers": clientServerNames(root, t.Format), "preview": string(block), "user_id": in.UserID, "days": in.Days, "never_expires": in.NeverExpires, "request_id": ID(), "gateway_url": a.PublicOrigin() + "/mcp"})
}
func (a *App) listClientIntegrations(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	rows, err := a.DB.Query(r.Context(), "SELECT value FROM system_settings WHERE key LIKE 'client_integration:%' ORDER BY key")
	if err != nil {
		fail(w, 503, "接入记录读取失败")
		return
	}
	var values []string
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			rows.Close()
			fail(w, 503, "接入记录读取失败")
			return
		}
		values = append(values, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		fail(w, 503, "接入记录读取失败")
		return
	}
	out := []any{}
	for _, value := range values {
		i, e := a.decodeIntegration(value)
		if e != nil {
			fail(w, 503, e.Error())
			return
		}
		if i.State == "removed" {
			continue
		}
		item := i.public()
		var last sql.NullString
		var revoked bool
		e = a.DB.QueryRow(r.Context(), "SELECT last_seen_at,revoked FROM tokens WHERE id=$1", i.TokenID).Scan(&last, &revoked)
		if e != nil {
			revoked = true
		}
		item["token_revoked"] = revoked
		if last.Valid {
			item["last_seen_at"] = last.String
		}
		out = append(out, item)
	}
	respond(w, 200, map[string]any{"items": out})
}

func (a *App) applyClientIntegration(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var in integrationRequest
	if !decode(w, r, &in) {
		return
	}
	if len(in.RequestID) != 32 || !clientNamePattern.MatchString(in.RequestID) {
		fail(w, 400, "请先预览接入配置")
		return
	}
	clientIntegrationMu.Lock()
	defer clientIntegrationMu.Unlock()
	saved, err := a.readIntegration(r.Context(), in.RequestID)
	if err == nil {
		if saved.State != "active" && saved.State != "pending" {
			fail(w, 409, "此接入操作已撤销，请重新预览")
			return
		}
		if err = a.finishClientIntegration(r.Context(), &saved); err != nil {
			fail(w, 409, err.Error())
			return
		}
		respond(w, 200, saved.public())
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		fail(w, 503, "接入记录不可读，请检查密钥")
		return
	}
	t, path, raw, exists, err := a.validateIntegrationRequest(r, &in)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if path != in.Path || fileFingerprint(raw, exists) != in.Fingerprint || in.GatewayURL != a.PublicOrigin()+"/mcp" {
		fail(w, 409, "配置文件或路径已改变，请重新预览")
		return
	}
	if !strings.HasPrefix(a.PublicOrigin(), "http://") && !strings.HasPrefix(a.PublicOrigin(), "https://") {
		fail(w, 503, "Gateway 接入地址尚未就绪")
		return
	}
	id, secret := ID(), token()
	entry := clientEntry(t.Format, a.PublicOrigin()+"/mcp", secret)
	_, old, err := editClientConfig(raw, t.Format, in.Name, entry)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if old != nil && !in.Replace {
		fail(w, 409, "已有同名 MCP；请更换名称，或确认替换后重试")
		return
	}
	backup := ""
	if exists {
		backup, err = privateClientBackup(path, raw)
		if err != nil {
			fail(w, 503, "备份失败，未修改客户端配置")
			return
		}
	}
	expires := time.Unix(0, 0).UTC()
	if !in.NeverExpires { expires = time.Now().UTC().Add(time.Duration(in.Days) * 24 * time.Hour) }
	i := clientIntegration{ID: in.RequestID, Client: t.ID, ClientName: t.Name, Path: path, Format: t.Format, Name: in.Name, UserID: in.UserID, TokenID: id, Backup: backup, Before: in.Fingerprint, Previous: old, Applied: entry, State: "pending", Created: time.Now().UTC(), NeverExpires: in.NeverExpires, Expires: expires}
	value, err := a.encodeIntegration(i)
	if err != nil {
		fail(w, 503, "接入记录加密失败")
		return
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		fail(w, 503, "数据库不可用")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "INSERT INTO tokens(id,user_id,client_name,hash,expires_at,revoked) VALUES($1,$2,$3,$4,$5,0)", id, in.UserID, t.Name+" · "+in.Name, hash(secret), time.Unix(0, 0).UTC()); err == nil {
		_, err = tx.Exec(r.Context(), "INSERT INTO system_settings(key,value) VALUES($1,$2)", integrationPrefix+i.ID, value)
	}
	if err != nil {
		fail(w, 503, "创建接入记录失败；未修改客户端配置")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		fail(w, 503, "保存接入记录失败；未修改客户端配置")
		return
	}
	if err = a.finishClientIntegration(r.Context(), &i); err != nil {
		slog.Warn("客户端接入待完成", "event", "client_integration_pending", "integration_id", i.ID, "detail", err.Error())
		fail(w, 503, err.Error()+"；可重试同一操作或在接入记录中撤销")
		return
	}
	slog.Info("客户端配置接入完成", "event", "client_integration_applied", "client", i.Client, "integration_id", i.ID)
	respond(w, 201, i.public())
}

// Pending journal is persisted before the config write, so retries after a crash
// reuse the same encrypted token and never issue duplicate credentials.
func (a *App) finishClientIntegration(ctx context.Context, i *clientIntegration) error {
	if i.State == "pending" {
		var permitted bool
		if err := a.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM tokens t JOIN users u ON u.id=t.user_id WHERE t.id=$1 AND NOT t.revoked AND NOT t.client_deleted AND u.enabled)", i.TokenID).Scan(&permitted); err != nil || !permitted {
			return errors.New("Token 已撤销或所属用户已停用，未修改配置，请撤销此记录")
		}
		if !i.NeverExpires && !i.Expires.After(time.Now()) {
			return errors.New("待完成接入已过期，未修改配置，请撤销后重新接入")
		}
	}
	raw, exists, err := readClientFile(i.Path)
	if err != nil {
		return errors.New("客户端配置不可读")
	}
	root, err := decodeClientConfig(raw, i.Format)
	if err != nil {
		return err
	}
	entry, err := clientServer(root, i.Format, i.Name)
	if err != nil {
		return err
	}
	if entryFingerprint(entry) != entryFingerprint(i.Applied) {
		if i.State == "active" {
			return errors.New("已接入配置被其他程序修改，请重新预览")
		}
		if fileFingerprint(raw, exists) != i.Before {
			return errors.New("配置在接入期间发生变化，请撤销待完成记录并重新预览")
		}
		proposed, _, e := editClientConfig(raw, i.Format, i.Name, i.Applied)
		if e != nil {
			return e
		}
		again, present, e := readClientFile(i.Path)
		if e != nil || fileFingerprint(again, present) != i.Before {
			return errors.New("写入前配置发生变化，请重新预览")
		}
		if e = atomicClientWrite(i.Path, proposed); e != nil {
			return errors.New("客户端配置写入失败，Token 尚未激活")
		}
	}
	if i.State == "active" {
		return nil
	}
	if !i.NeverExpires && !i.Expires.After(time.Now()) {
		return errors.New("待完成接入已过期，请撤销后重新接入")
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return errors.New("配置已写入，但 Token 激活失败，请重试")
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, "UPDATE tokens SET expires_at=$3,never_expires=$4 WHERE id=$1 AND NOT revoked AND NOT client_deleted AND EXISTS(SELECT 1 FROM users WHERE id=$2 AND enabled)", i.TokenID, i.UserID, i.Expires, i.NeverExpires)
	if err != nil || tag.RowsAffected() != 1 {
		return errors.New("所属用户已停用或 Token 已撤销，请撤销此记录")
	}
	i.State = "active"
	value, err := a.encodeIntegration(*i)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE system_settings SET value=$2 WHERE key=$1", integrationPrefix+i.ID, value); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO management_audit(entity_type,entity_id,action) VALUES('client_integrations',$1,'APPLY')", i.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (a *App) removeClientIntegration(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	clientIntegrationMu.Lock()
	defer clientIntegrationMu.Unlock()
	i, err := a.readIntegration(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 404, "接入记录不存在或无法解密")
		return
	}
	if i.State == "removed" {
		respond(w, 200, map[string]bool{"ok": true})
		return
	}
	raw, _, err := readClientFile(i.Path)
	if err != nil {
		fail(w, 409, "配置不可读，请先在 Gateway 页面撤销对应 Token")
		return
	}
	root, err := decodeClientConfig(raw, i.Format)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	current, err := clientServer(root, i.Format, i.Name)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	fp := entryFingerprint(current)
	if fp != entryFingerprint(i.Applied) && fp != entryFingerprint(i.Previous) {
		fail(w, 409, "同名 MCP 已被手工修改，未覆盖；请先在 Gateway 页面撤销 Token，并手动移除客户端配置")
		return
	}
	// Revoke first, retain the original entry and journal for an idempotent retry.
	i.State = "removing"
	if _, err = a.DB.Exec(r.Context(), "UPDATE tokens SET revoked=1,client_deleted=1 WHERE id=$1", i.TokenID); err != nil {
		fail(w, 503, "撤销 Token 失败，未修改配置")
		return
	}
	if err = a.saveIntegration(r.Context(), i); err != nil {
		fail(w, 503, "Token 已撤销，但移除记录保存失败，可重试")
		return
	}
	if fp == entryFingerprint(i.Applied) {
		proposed, _, e := editClientConfig(raw, i.Format, i.Name, i.Previous)
		if e != nil {
			fail(w, 409, e.Error())
			return
		}
		again, _, e := readClientFile(i.Path)
		if e != nil || fileFingerprint(raw, true) != fileFingerprint(again, true) {
			fail(w, 409, "Token 已撤销，配置同时被其他程序修改；可重试移除")
			return
		}
		if e = atomicClientWrite(i.Path, proposed); e != nil {
			fail(w, 503, "Token 已撤销，配置移除失败；可重试移除")
			return
		}
	}
	i.State = "removed"
	if err = a.saveIntegration(r.Context(), i); err != nil {
		fail(w, 503, "配置已还原，记录保存失败；可重试")
		return
	}
	a.closeIntegrationLeases(i.TokenID)
	slog.Info("客户端一键接入已撤销", "event", "client_integration_removed", "integration_id", i.ID)
	respond(w, 200, map[string]bool{"ok": true})
}

func (a *App) verifyClientIntegrationSecrets(ctx context.Context) error {
	rows, err := a.DB.Query(ctx, "SELECT value FROM system_settings WHERE key LIKE 'client_integration:%'")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			return err
		}
		if _, err = a.decodeIntegration(value); err != nil {
			return errors.New("MASTER_KEY cannot decrypt client integration records")
		}
	}
	return rows.Err()
}

func (a *App) closeIntegrationLeases(tokenID string) {
	a.gatewayMu.Lock()
	var leases []*gatewayLease
	for id, lease := range a.gatewayLeases {
		if lease.tokenID == tokenID {
			delete(a.gatewayLeases, id)
			leases = append(leases, lease)
		}
	}
	a.gatewayMu.Unlock()
	for _, lease := range leases {
		lease.close()
	}
}
