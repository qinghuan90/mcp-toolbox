package platform

import (
	"context"
	"net/http"
	"strings"
)

type toolGrant struct {
	InstanceID string   `json:"instance_id"`
	Tools      []string `json:"tools"`
}
type accessRole struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Version     int         `json:"version"`
	Scope       string      `json:"scope"`
	InstanceIDs []string    `json:"instance_ids"`
	Grants      []toolGrant `json:"grants"`
	UserIDs     []string    `json:"user_ids"`
}

// An all role includes future services and tools. Custom grants are exact names;
// '*' grants every current and future tool of only that service.
const serviceRolePermission = `(u.role='admin' OR EXISTS(SELECT 1 FROM user_roles ur JOIN access_roles ar ON ar.id=ur.role_id WHERE ur.user_id=u.id AND (ar.all_mcp OR EXISTS(SELECT 1 FROM role_tools rt WHERE rt.role_id=ar.id AND rt.instance_id=$2))))`
const toolRolePermission = `(u.role='admin' OR EXISTS(SELECT 1 FROM user_roles ur JOIN access_roles ar ON ar.id=ur.role_id WHERE ur.user_id=u.id AND (ar.all_mcp OR EXISTS(SELECT 1 FROM role_tools rt WHERE rt.role_id=ar.id AND rt.instance_id=$2 AND (rt.tool_name='*' OR rt.tool_name=$3)))))`

func (a *App) canUseInstance(ctx context.Context, userID, instanceID string) bool {
	var yes bool
	err := a.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users u WHERE u.id=$1 AND u.enabled AND NOT u.must_change_password AND `+serviceRolePermission+`)`, userID, instanceID).Scan(&yes)
	return err == nil && yes
}
func (a *App) canUseTool(ctx context.Context, userID, instanceID, tool string) bool {
	var yes bool
	err := a.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users u WHERE u.id=$1 AND u.enabled AND NOT u.must_change_password AND `+toolRolePermission+`)`, userID, instanceID, tool).Scan(&yes)
	return err == nil && yes
}
func (a *App) rolesAPI(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx := r.Context()
	if r.Method == http.MethodGet {
		rows, err := a.DB.Query(ctx, "SELECT id,name,description,version,all_mcp FROM access_roles ORDER BY name")
		if err != nil {
			fail(w, 503, "角色查询失败")
			return
		}
		out := []accessRole{}
		for rows.Next() {
			var role accessRole
			var all bool
			if err = rows.Scan(&role.ID, &role.Name, &role.Description, &role.Version, &all); err != nil {
				break
			}
			role.Scope = "custom"
			if all {
				role.Scope = "all"
			}
			role.InstanceIDs = []string{}
			role.Grants = []toolGrant{}
			role.UserIDs = []string{}
			out = append(out, role)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			fail(w, 503, "角色查询失败")
			return
		}
		for i := range out {
			rows, err := a.DB.Query(ctx, "SELECT instance_id,tool_name FROM role_tools WHERE role_id=$1 ORDER BY instance_id,tool_name", out[i].ID)
			if err != nil {
				fail(w, 503, "角色查询失败")
				return
			}
			for rows.Next() {
				var instance, tool string
				if err = rows.Scan(&instance, &tool); err != nil {
					break
				}
				n := len(out[i].Grants)
				if n == 0 || out[i].Grants[n-1].InstanceID != instance {
					out[i].InstanceIDs = append(out[i].InstanceIDs, instance)
					out[i].Grants = append(out[i].Grants, toolGrant{InstanceID: instance, Tools: []string{}})
					n = len(out[i].Grants)
				}
				out[i].Grants[n-1].Tools = append(out[i].Grants[n-1].Tools, tool)
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
			if err != nil {
				fail(w, 503, "角色查询失败")
				return
			}
			rows, err = a.DB.Query(ctx, "SELECT user_id FROM user_roles WHERE role_id=$1 ORDER BY user_id", out[i].ID)
			if err != nil {
				fail(w, 503, "角色查询失败")
				return
			}
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					break
				}
				out[i].UserIDs = append(out[i].UserIDs, id)
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
			if err != nil {
				fail(w, 503, "角色查询失败")
				return
			}
		}
		respond(w, 200, map[string]any{"items": out, "total": len(out)})
		return
	}
	var in accessRole
	if !decode(w, r, &in) {
		return
	}
	if r.Method != http.MethodDelete {
		in.Name = strings.TrimSpace(in.Name)
		if in.Name == "" || len(in.Name) > 100 || len(in.Description) > 2000 || len(in.Grants) > 2000 || len(in.InstanceIDs) > 2000 || len(in.UserIDs) > 2000 {
			fail(w, 400, "角色名称或授权数量无效")
			return
		}
		if in.Scope != "" && in.Scope != "all" && in.Scope != "custom" {
			fail(w, 400, "授权范围必须是 all 或 custom")
			return
		}
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		fail(w, 503, "数据库不可用")
		return
	}
	defer tx.Rollback(ctx)
	id := r.PathValue("id")
	action := "UPDATE"
	oldAll := false
	if r.Method == http.MethodPost {
		id = ID()
		action = "INSERT"
		if in.Scope == "" {
			in.Scope = "all"
			if in.Grants != nil || in.InstanceIDs != nil {
				in.Scope = "custom"
			}
		}
	} else {
		var version int
		if err = tx.QueryRow(ctx, "SELECT version,all_mcp FROM access_roles WHERE id=$1", id).Scan(&version, &oldAll); err != nil {
			fail(w, 404, "角色不存在")
			return
		}
		if in.Version != version {
			fail(w, 409, "角色已被其他操作更新，请刷新后重试")
			return
		}
		if in.Scope == "" {
			in.Scope = "custom"
			if oldAll {
				in.Scope = "all"
			}
		}
	}
	if r.Method == http.MethodDelete {
		action = "DELETE"
		_, err = tx.Exec(ctx, "DELETE FROM access_roles WHERE id=$1", id)
	} else {
		if in.Grants == nil {
			for _, instanceID := range in.InstanceIDs {
				in.Grants = append(in.Grants, toolGrant{InstanceID: instanceID, Tools: []string{"*"}})
			}
		}
		if in.Scope == "all" {
			in.Grants = nil
		}
		// Check specific tools before replacing grants so stopped servers can retain
		// previously approved names without allowing newly invented tool names.
		seen := map[string]bool{}
		total := 0
		for index := range in.Grants {
			grant := &in.Grants[index]
			if grant.InstanceID == "" || seen[grant.InstanceID] || len(grant.Tools) == 0 {
				fail(w, 400, "请选择 MCP 和对应工具，MCP 不可重复")
				return
			}
			seen[grant.InstanceID] = true
			known := map[string]bool{}
			if v, ok := a.Runtime.Load(grant.InstanceID); ok {
				for _, tool := range v.(*snapshot).tools {
					known[tool.Name] = true
				}
			}
			unique := []string{}
			names := map[string]bool{}
			for _, tool := range grant.Tools {
				total++
				if total > 20000 || tool == "" || len(tool) > 512 {
					fail(w, 400, "工具名称或数量无效")
					return
				}
				if tool != "*" && !known[tool] {
					var saved bool
					if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM role_tools WHERE role_id=$1 AND instance_id=$2 AND tool_name=$3)", id, grant.InstanceID, tool).Scan(&saved); err != nil {
						fail(w, 503, "授权检查失败")
						return
					}
					if !saved {
						fail(w, 400, "指定工具不存在或 MCP 尚未运行，请启动服务并刷新工具列表")
						return
					}
				}
				if !names[tool] {
					unique = append(unique, tool)
					names[tool] = true
				}
			}
			if names["*"] {
				unique = []string{"*"}
			}
			grant.Tools = unique
		}
		if r.Method == http.MethodPost {
			_, err = tx.Exec(ctx, "INSERT INTO access_roles(id,name,description,all_mcp) VALUES($1,$2,$3,$4)", id, in.Name, in.Description, in.Scope == "all")
		} else {
			_, err = tx.Exec(ctx, "UPDATE access_roles SET name=$2,description=$3,all_mcp=$4,version=version+1 WHERE id=$1", id, in.Name, in.Description, in.Scope == "all")
		}
		if err != nil {
			fail(w, 409, "角色名称已存在或保存失败")
			return
		}
		if _, err = tx.Exec(ctx, "DELETE FROM role_instances WHERE role_id=$1", id); err != nil {
			fail(w, 503, "保存授权失败")
			return
		}
		if _, err = tx.Exec(ctx, "DELETE FROM user_roles WHERE role_id=$1", id); err != nil {
			fail(w, 503, "保存成员失败")
			return
		}
		for _, grant := range in.Grants {
			if _, err = tx.Exec(ctx, "INSERT INTO role_instances(role_id,instance_id) VALUES($1,$2)", id, grant.InstanceID); err != nil {
				fail(w, 400, "所选 MCP 服务不存在，请刷新后重试")
				return
			}
			for _, tool := range grant.Tools {
				if _, err = tx.Exec(ctx, "INSERT INTO role_tools(role_id,instance_id,tool_name) VALUES($1,$2,$3)", id, grant.InstanceID, tool); err != nil {
					fail(w, 503, "保存工具授权失败")
					return
				}
			}
		}
		members := map[string]bool{}
		for _, user := range in.UserIDs {
			if members[user] {
				continue
			}
			members[user] = true
			if _, err = tx.Exec(ctx, "INSERT INTO user_roles(role_id,user_id) VALUES($1,$2)", id, user); err != nil {
				fail(w, 400, "所选用户不存在，请刷新后重试")
				return
			}
		}
	}
	if err != nil {
		fail(w, 503, "保存失败")
		return
	}
	if _, err = tx.Exec(ctx, "INSERT INTO management_audit(entity_type,entity_id,action) VALUES('access_roles',$1,$2)", id, action); err != nil {
		fail(w, 503, "保存审计失败")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		fail(w, 503, "保存失败")
		return
	}
	respond(w, 200, map[string]any{"id": id, "ok": true})
}
