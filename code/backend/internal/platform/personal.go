package platform

import (
	"context"
	"net/http"
	"strings"
)

type personalRequestKey struct{}

// The database identity preserves Token ownership and audit history. It is not
// an interactive account; personal access is granted only inside the Go process.
func (a *App) BootstrapPersonalOwner(ctx context.Context) error {
	var count int
	if err := a.DB.QueryRow(ctx, "SELECT count(*) FROM users WHERE role='admin' AND enabled=1").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	id := ID()
	_, err := a.DB.Exec(ctx, "INSERT INTO users(id,username,password_hash,role) VALUES($1,$2,'!personal-only','admin')", id, "local-owner-"+id)
	return err
}

func (a *App) personalOwner(ctx context.Context) (User, error) {
	var u User
	err := a.DB.QueryRow(ctx, "SELECT id FROM users WHERE role='admin' AND enabled=1 ORDER BY CASE WHEN username='toolbox' THEN 0 ELSE 1 END,id LIMIT 1").Scan(&u.ID)
	u.Username, u.Role, u.Enabled, u.Personal = "本机管理员", "admin", true, true
	return u, err
}

// PersonalHandler is wired exclusively to Wails' in-process asset bridge.
// The TCP listener uses Handler without this private context capability.
func (a *App) PersonalHandler() http.Handler {
	handler := a.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Personal {
			fail(w, 403, "当前不是个人版本")
			return
		}
		handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), personalRequestKey{}, true)))
	})
}

func personalAccountRoute(path string) bool {
	return path == "/api/v1/setup" || path == "/api/v1/login" || path == "/api/v1/logout" || path == "/api/v1/account/password" ||
		path == "/api/v1/users" || strings.HasPrefix(path, "/api/v1/users/") ||
		path == "/api/v1/roles" || strings.HasPrefix(path, "/api/v1/roles/")
}
