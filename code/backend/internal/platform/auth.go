package platform

import (
	"context"
	"crypto/subtle"
	"golang.org/x/crypto/bcrypt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultAdminUsername = "toolbox"
const defaultAdminPassword = "toolbox@2026"

func (a *App) BootstrapLocalAdmin(ctx context.Context) error {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	hashValue, err := bcrypt.GenerateFromPassword([]byte(defaultAdminPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO users(id,username,password_hash,role,must_change_password) VALUES($1,$2,$3,'admin',1)", ID(), defaultAdminUsername, string(hashValue)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type userKey struct{}

func current(r *http.Request) User { return r.Context().Value(userKey{}).(User) }
func (a *App) limited(r *http.Request) bool {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	a.rateMu.Lock()
	defer a.rateMu.Unlock()
	now := time.Now()
	if len(a.attempts) > 10000 {
		for k, v := range a.attempts {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > time.Minute {
				delete(a.attempts, k)
			}
		}
		if len(a.attempts) > 10000 {
			return true
		}
	}
	var v []time.Time
	for _, t := range a.attempts[ip] {
		if now.Sub(t) < time.Minute {
			v = append(v, t)
		}
	}
	a.attempts[ip] = append(v, now)
	return len(v) >= 30
}
func (a *App) originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	u, e := url.Parse(o)
	origin := a.PublicOrigin()
	if e != nil {
		return false
	}
	if origin != "" && o == origin {
		return true
	}
	if origin == "" {
		return u.Host == r.Host
	}
	// A local binary may advertise 127.0.0.1 while the browser uses localhost.
	// Both names are safe only for the same HTTP listener and browser origin.
	configured, err := url.Parse(origin)
	return err == nil && configured.Scheme == "http" && u.Scheme == "http" &&
		loopbackHost(configured.Hostname()) && loopbackHost(u.Hostname()) &&
		configured.Port() != "" && u.Port() == configured.Port() &&
		u.Host == r.Host && u.User == nil && u.RawQuery == "" && u.Fragment == "" &&
		(u.Path == "" || u.Path == "/")
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func (a *App) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var u User
		if a.Personal {
			if allowed, _ := r.Context().Value(personalRequestKey{}).(bool); !allowed {
				fail(w, 401, "管理功能仅可在个人桌面应用中使用")
				return
			}
			var err error
			u, err = a.personalOwner(r.Context())
			if err != nil {
				fail(w, 503, "本机管理身份不可用，请检查数据库")
				return
			}
		} else {
			c, e := r.Cookie("toolbox_session")
			if e != nil {
				fail(w, 401, "请登录")
				return
			}
			e = a.DB.QueryRow(r.Context(), "SELECT u.id,u.username,u.role,u.enabled,u.must_change_password FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.hash=$1 AND julianday(s.expires_at)>julianday('now') AND u.enabled", hash(c.Value)).Scan(&u.ID, &u.Username, &u.Role, &u.Enabled, &u.MustChangePassword)
			if e != nil {
				fail(w, 401, "会话已失效")
				return
			}
		}
		if u.MustChangePassword && r.URL.Path != "/api/v1/me" && r.URL.Path != "/api/v1/account/password" && r.URL.Path != "/api/v1/logout" {
			fail(w, 403, "首次登录须先修改初始密码")
			return
		}
		if r.Method != "GET" {
			if !a.originOK(r) {
				fail(w, 403, "请求来源不允许")
				return
			}
			if u.Role != "admin" && r.URL.Path != "/api/v1/logout" && r.URL.Path != "/api/v1/account/password" {
				fail(w, 403, "需要管理员权限")
				return
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
	}
}
func (a *App) setup(w http.ResponseWriter, r *http.Request) {
	var n int
	a.DB.QueryRow(r.Context(), "SELECT count(*) FROM users").Scan(&n)
	if r.Method == "GET" {
		respond(w, 200, map[string]bool{"required": n == 0})
		return
	}
	if a.limited(r) {
		fail(w, 429, "尝试过于频繁")
		return
	}
	var in struct {
		Username, Password string
		SetupKey           string `json:"setup_key"`
	}
	if !decode(w, r, &in) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(in.SetupKey), []byte(a.SetupKey)) != 1 || len(in.Password) < 12 || len(in.Password) > 72 || in.Password == defaultAdminPassword || strings.TrimSpace(in.Username) == "" {
		fail(w, 400, "初始化凭据无效，密码须为12–72字节且不能使用初始密码")
		return
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		fail(w, 503, "数据库不可用")
		return
	}
	defer tx.Rollback(r.Context())
	tx.QueryRow(r.Context(), "SELECT count(*) FROM users").Scan(&n)
	if n != 0 {
		fail(w, 409, "初始化已完成")
		return
	}
	p, _ := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	_, e = tx.Exec(r.Context(), "INSERT INTO users(id,username,password_hash,role) VALUES($1,$2,$3,'admin')", ID(), in.Username, string(p))
	if e != nil {
		fail(w, 400, "无法创建用户")
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		fail(w, 503, "保存失败")
		return
	}
	respond(w, 201, map[string]bool{"ok": true})
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if !a.originOK(r) {
		fail(w, 403, "请求来源不允许")
		return
	}
	if a.limited(r) {
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "尝试过于频繁")
		return
	}
	var in struct{ Username, Password string }
	if !decode(w, r, &in) {
		return
	}
	var u User
	var p string
	var locked bool
	e := a.DB.QueryRow(r.Context(), "SELECT id,username,role,enabled,must_change_password,password_hash,COALESCE(julianday(locked_until)>julianday('now'),0) FROM users WHERE username=$1", in.Username).Scan(&u.ID, &u.Username, &u.Role, &u.Enabled, &u.MustChangePassword, &p, &locked)
	if locked {
		fail(w, 429, "登录暂时受限，请稍后重试")
		return
	}
	if e != nil || !u.Enabled || bcrypt.CompareHashAndPassword([]byte(p), []byte(in.Password)) != nil {
		a.DB.Exec(r.Context(), "UPDATE users SET failures=failures+1,locked_until=CASE WHEN failures+1>=5 THEN datetime('now','+5 minutes') ELSE locked_until END WHERE username=$1", in.Username)
		fail(w, 401, "用户名或密码错误")
		return
	}
	s := token()
	_, e = a.DB.Exec(r.Context(), "INSERT INTO sessions(hash,user_id,expires_at) VALUES($1,$2,$3)", hash(s), u.ID, time.Now().Add(12*time.Hour))
	if e != nil {
		fail(w, 503, "会话创建失败")
		return
	}
	a.DB.Exec(r.Context(), "UPDATE users SET failures=0,locked_until=NULL WHERE id=$1", u.ID)
	http.SetCookie(w, &http.Cookie{Name: "toolbox_session", Value: s, Path: "/", HttpOnly: true, Secure: a.Secure, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	respond(w, 200, u)
}
func (a *App) changePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.NewPassword) < 12 || len(in.NewPassword) > 72 || in.NewPassword == defaultAdminPassword {
		fail(w, 400, "新密码须为12–72字节且不能使用初始密码")
		return
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		fail(w, 503, "数据库不可用")
		return
	}
	defer tx.Rollback(r.Context())
	var oldHash string
	if err = tx.QueryRow(r.Context(), "SELECT password_hash FROM users WHERE id=$1 AND enabled", current(r).ID).Scan(&oldHash); err != nil {
		fail(w, 401, "账号不可用")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(oldHash), []byte(in.CurrentPassword)) != nil {
		fail(w, 400, "当前密码错误")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(oldHash), []byte(in.NewPassword)) == nil {
		fail(w, 400, "新密码不能与当前密码相同")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(in.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		fail(w, 503, "密码处理失败")
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE users SET password_hash=$2,must_change_password=0,failures=0,locked_until=NULL WHERE id=$1", current(r).ID, string(newHash)); err != nil {
		fail(w, 503, "密码保存失败")
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM sessions WHERE user_id=$1", current(r).ID); err != nil {
		fail(w, 503, "会话失效失败")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		fail(w, 503, "密码保存失败")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "toolbox_session", Value: "", Path: "/", HttpOnly: true, Secure: a.Secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	respond(w, 200, map[string]bool{"ok": true})
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Cookie("toolbox_session")
	if c != nil {
		if _, e := a.DB.Exec(r.Context(), "DELETE FROM sessions WHERE hash=$1", hash(c.Value)); e != nil {
			fail(w, 503, "退出失败，请重试")
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: "toolbox_session", Value: "", Path: "/", HttpOnly: true, Secure: a.Secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	respond(w, 200, map[string]bool{"ok": true})
}
