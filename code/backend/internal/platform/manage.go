package platform

import (
	"database/sql"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

func (a *App) usersAPI(w http.ResponseWriter, r *http.Request) {
	if current(r).Role != "admin" {
		fail(w, 403, "需要管理员权限")
		return
	}
	if r.Method == "GET" {
		rows, e := a.DB.Query(r.Context(), "SELECT id,username,role,enabled,must_change_password FROM users ORDER BY username")
		if e != nil {
			fail(w, 503, "查询失败")
			return
		}
		defer rows.Close()
		out := []User{}
		for rows.Next() {
			var u User
			rows.Scan(&u.ID, &u.Username, &u.Role, &u.Enabled, &u.MustChangePassword)
			out = append(out, u)
		}
		respond(w, 200, map[string]any{"items": out, "total": len(out)})
		return
	}
	var in struct {
		Username string
		Password string
		Role     string
		Enabled  *bool
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Role != "" && in.Role != "admin" && in.Role != "user" {
		fail(w, 400, "角色无效")
		return
	}
	if r.Method == "POST" {
		if len(in.Password) < 12 || len(in.Password) > 72 || in.Password == defaultAdminPassword || strings.TrimSpace(in.Username) == "" {
			fail(w, 400, "用户名不能为空，密码须为12–72字节且不能使用初始密码")
			return
		}
		if in.Role == "" {
			in.Role = "user"
		}
		p, _ := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		id := ID()
		_, e := a.DB.Exec(r.Context(), "INSERT INTO users(id,username,password_hash,role) VALUES($1,$2,$3,$4)", id, in.Username, string(p), in.Role)
		if e != nil {
			fail(w, 409, "用户名已存在或保存失败")
			return
		}
		respond(w, 201, User{ID: id, Username: in.Username, Role: in.Role, Enabled: true})
		return
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		fail(w, 503, "数据库不可用")
		return
	}
	defer tx.Rollback(r.Context())
	var u User
	e = tx.QueryRow(r.Context(), "SELECT id,username,role,enabled,must_change_password FROM users WHERE id=$1", r.PathValue("id")).Scan(&u.ID, &u.Username, &u.Role, &u.Enabled, &u.MustChangePassword)
	if e != nil {
		fail(w, 404, "用户不存在")
		return
	}
	role, enabled := u.Role, u.Enabled
	if in.Role != "" {
		role = in.Role
	}
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	if u.Role == "admin" && u.Enabled && (role != "admin" || !enabled) {
		var n int
		tx.QueryRow(r.Context(), "SELECT count(*) FROM users WHERE role='admin' AND enabled").Scan(&n)
		if n <= 1 {
			fail(w, 409, "不能停用或降级最后一个管理员")
			return
		}
	}
	_, e = tx.Exec(r.Context(), "UPDATE users SET role=$2,enabled=$3 WHERE id=$1", u.ID, role, enabled)
	if in.Password != "" {
		if len(in.Password) < 12 || len(in.Password) > 72 || in.Password == defaultAdminPassword {
			fail(w, 400, "密码须为12–72字节且不能使用初始密码")
			return
		}
		p, _ := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		_, e = tx.Exec(r.Context(), "UPDATE users SET password_hash=$2,must_change_password=1,failures=0,locked_until=NULL WHERE id=$1", u.ID, string(p))
		tx.Exec(r.Context(), "DELETE FROM sessions WHERE user_id=$1", u.ID)
	}
	if e != nil || tx.Commit(r.Context()) != nil {
		fail(w, 503, "更新失败")
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
func (a *App) tokensAPI(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	if r.Method == "GET" {
		query := "SELECT id,user_id,client_name,expires_at,revoked,created_at,last_seen_at,never_expires FROM tokens WHERE ($1='admin' OR user_id=$2)"
		if r.URL.Path == "/api/v1/clients" {
			query += " AND NOT client_deleted"
		}
		query += " ORDER BY created_at DESC"
		rows, e := a.DB.Query(r.Context(), query, u.Role, u.ID)
		if e != nil {
			fail(w, 503, "查询失败")
			return
		}
		defer rows.Close()
		out := []any{}
		for rows.Next() {
			var id, uid, client string
			var expires, created string
			var revoked, neverExpires bool
			var last sql.NullString
			rows.Scan(&id, &uid, &client, &expires, &revoked, &created, &last, &neverExpires)
			var lastSeen any
			if last.Valid {
				lastSeen = last.String
			}
			out = append(out, map[string]any{"id": id, "user_id": uid, "client_name": client, "expires_at": expires, "revoked": revoked, "created_at": created, "last_seen_at": lastSeen, "never_expires": neverExpires})
		}
		respond(w, 200, map[string]any{"items": out, "total": len(out)})
		return
	}
	if r.Method == "DELETE" {
		tag, e := a.DB.Exec(r.Context(), "UPDATE tokens SET revoked=true WHERE id=$1", r.PathValue("id"))
		if e != nil || tag.RowsAffected() == 0 {
			fail(w, 404, "Token 不存在")
			return
		}
		a.closeGatewayLeases()
		respond(w, 200, map[string]bool{"ok": true})
		return
	}
	var in struct {
		UserID       string    `json:"user_id"`
		Client       string    `json:"client_name"`
		Expires      time.Time `json:"expires_at"`
		NeverExpires bool      `json:"never_expires"`
	}
	if !decode(w, r, &in) {
		return
	}
	if (!in.NeverExpires && !in.Expires.After(time.Now())) || strings.TrimSpace(in.Client) == "" {
		fail(w, 400, "需要客户端名称和未来有效期")
		return
	}
	if a.Personal {
		in.UserID = u.ID
	}
	if in.NeverExpires {
		in.Expires = time.Unix(0, 0).UTC()
	}
	id, v := ID(), token()
	tag, e := a.DB.Exec(r.Context(), "INSERT INTO tokens(id,user_id,client_name,hash,expires_at,never_expires) SELECT $1,id,$3,$4,$5,$6 FROM users WHERE id=$2 AND enabled", id, in.UserID, in.Client, hash(v), in.Expires, in.NeverExpires)
	if e != nil || tag.RowsAffected() == 0 {
		fail(w, 400, "用户不可用")
		return
	}
	respond(w, 201, map[string]any{"id": id, "token": v, "user_id": in.UserID, "client_name": in.Client, "expires_at": in.Expires, "never_expires": in.NeverExpires})
}
func (a *App) rulesAPI(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method == "GET" {
		rows, e := a.DB.Query(r.Context(), "SELECT id,cidr,effect FROM ip_rules ORDER BY cidr,effect")
		if e != nil {
			fail(w, 503, "查询失败")
			return
		}
		defer rows.Close()
		out := []any{}
		for rows.Next() {
			var id, cidr, effect string
			if err := rows.Scan(&id, &cidr, &effect); err != nil {
				fail(w, 503, "查询失败")
				return
			}
			out = append(out, map[string]string{"id": id, "cidr": cidr, "effect": effect})
		}
		respond(w, 200, map[string]any{"items": out, "total": len(out)})
		return
	}
	if r.Method == "DELETE" {
		if _, e := a.DB.Exec(r.Context(), "DELETE FROM ip_rules WHERE id=$1", r.PathValue("id")); e != nil {
			fail(w, 503, "删除失败")
			return
		}
		a.closeGatewayLeases()
		respond(w, 200, map[string]bool{"ok": true})
		return
	}
	var in struct {
		CIDR   string `json:"cidr"`
		Effect string `json:"effect"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Effect != "allow" && in.Effect != "deny" {
		fail(w, 400, "规则无效")
		return
	}
	cidr := strings.TrimSpace(in.CIDR)
	if address, err := netip.ParseAddr(cidr); err == nil {
		cidr = netip.PrefixFrom(address.Unmap(), address.Unmap().BitLen()).String()
	} else {
		prefix, parseErr := netip.ParsePrefix(cidr)
		if parseErr != nil || !prefix.IsValid() {
			fail(w, 400, "请输入有效的 IP 地址或 CIDR 网段")
			return
		}
		if prefix.Addr().Is4In6() {
			fail(w, 400, "IPv4 映射地址请填写普通 IPv4 地址或网段")
			return
		}
		cidr = prefix.Masked().String()
	}
	id := ID()
	_, e := a.DB.Exec(r.Context(), "INSERT INTO ip_rules(id,cidr,effect) VALUES($1,$2,$3)", id, cidr, in.Effect)
	if e != nil {
		fail(w, 409, "IP 规则重复或保存失败")
		return
	}
	a.closeGatewayLeases()
	respond(w, 201, map[string]string{"id": id})
}
func (a *App) logsAPI(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	q := r.URL.Query()
	limit, offset := pagination(r)
	var from, to *time.Time
	for key, target := range map[string]**time.Time{"from": &from, "to": &to} {
		if value := q.Get(key); value != "" {
			parsed, e := time.Parse(time.RFC3339, value)
			if e != nil {
				fail(w, 400, "时间需为 RFC3339")
				return
			}
			*target = &parsed
		}
	}
	where := ` FROM logs WHERE ($1='admin' OR user_id=$2) AND ($3='' OR status=$3) AND ($4='' OR user_id=$4) AND ($5='' OR client_name=$5) AND ($6='' OR instance_id=$6) AND ($7='' OR tool_name=$7) AND ($8 IS NULL OR julianday(created_at) >= julianday($8)) AND ($9 IS NULL OR julianday(created_at) <= julianday($9)) AND ($10='' OR token_id=$10)`
	args := []any{u.Role, u.ID, q.Get("status"), q.Get("user_id"), q.Get("client_name"), q.Get("instance_id"), q.Get("tool_name"), from, to, q.Get("token_id")}
	var total int
	if e := a.DB.QueryRow(r.Context(), "SELECT count(*)"+where, args...).Scan(&total); e != nil {
		fail(w, 503, "查询失败")
		return
	}
	rows, e := a.DB.Query(r.Context(), "SELECT id,user_id,token_id,client_name,instance_id,tool_name,status,detail,duration_ms,created_at"+where+" ORDER BY created_at DESC,id LIMIT $11 OFFSET $12", append(args, limit, offset)...)
	if e != nil {
		fail(w, 503, "查询失败")
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var id, client, t, status, d string
		var uid, tokenID, i *string
		var ms int64
		var at string
		if e := rows.Scan(&id, &uid, &tokenID, &client, &i, &t, &status, &d, &ms, &at); e != nil {
			fail(w, 503, "查询失败")
			return
		}
		out = append(out, map[string]any{"id": id, "user_id": uid, "token_id": tokenID, "client_name": client, "instance_id": i, "tool_name": t, "status": status, "detail": d, "duration_ms": ms, "created_at": at})
	}
	if rows.Err() != nil {
		fail(w, 503, "查询失败")
		return
	}
	respond(w, 200, map[string]any{"items": out, "total": total, "limit": limit, "offset": offset})
}
func pagination(r *http.Request) (int, int) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
