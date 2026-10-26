package platform

import "net/http"

// Keep the token row for historical log attribution; removal always revokes it.
func (a *App) deleteClient(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		fail(w, 503, "数据库不可用")
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), "UPDATE tokens SET revoked=true,client_deleted=true WHERE id=$1 AND NOT client_deleted", id)
	if err != nil {
		fail(w, 503, "删除客户端失败")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, 404, "客户端不存在或已删除")
		return
	}
	if _, err = tx.Exec(r.Context(), "INSERT INTO management_audit(entity_type,entity_id,action) VALUES('clients',$1,'DELETE')", id); err != nil {
		fail(w, 503, "保存审计失败")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		fail(w, 503, "保存失败")
		return
	}
	a.gatewayMu.Lock()
	var leases []*gatewayLease
	for sessionID, lease := range a.gatewayLeases {
		if lease.tokenID == id {
			delete(a.gatewayLeases, sessionID)
			leases = append(leases, lease)
		}
	}
	a.gatewayMu.Unlock()
	for _, lease := range leases {
		lease.close()
	}
	respond(w, 200, map[string]bool{"ok": true})
}
