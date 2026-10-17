package main

import (
	"net/http"
	"strings"
)

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `SELECT g.id,g.name,g.active,coalesce(array_agg(gm.user_id::text) FILTER (WHERE gm.user_id IS NOT NULL),'{}') FROM workflow_groups g LEFT JOIN workflow_group_members gm ON gm.group_id=g.id GROUP BY g.id ORDER BY g.name`)
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name string
		var active bool
		var members []string
		if err = rows.Scan(&id, &name, &active, &members); err != nil {
			errDB(w, err)
			return
		}
		out = append(out, map[string]any{"id": id, "name": name, "active": active, "member_ids": members})
	}
	if err = rows.Err(); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, out)
}
func (s *Server) saveGroup(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	var in struct {
		Name      string   `json:"name"`
		Active    bool     `json:"active"`
		MemberIDs []string `json:"member_ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 100 || len(in.MemberIDs) > 200 {
		fail(w, 400, "Укажите название группы до 100 символов и не более 200 участников")
		return
	}
	seen := map[string]bool{}
	for _, id := range in.MemberIDs {
		if !goodID(id) || seen[id] {
			fail(w, 400, "Некорректный список участников")
			return
		}
		seen[id] = true
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	id := r.PathValue("id")
	if r.Method == "POST" {
		err = tx.QueryRow(r.Context(), "INSERT INTO workflow_groups(name,active) VALUES($1,$2) RETURNING id", in.Name, in.Active).Scan(&id)
	} else {
		var tag interface{ RowsAffected() int64 }
		tag, err = tx.Exec(r.Context(), "UPDATE workflow_groups SET name=$2,active=$3 WHERE id=$1", id, in.Name, in.Active)
		if err == nil && tag.RowsAffected() == 0 {
			fail(w, 404, "Группа не найдена")
			return
		}
	}
	if err != nil {
		errDB(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM workflow_group_members WHERE group_id=$1", id); err != nil {
		errDB(w, err)
		return
	}
	for _, uid := range in.MemberIDs {
		tag, e := tx.Exec(r.Context(), "INSERT INTO workflow_group_members(group_id,user_id) SELECT $1,id FROM users WHERE id=$2 AND active=true", id, uid)
		if e != nil {
			errDB(w, e)
			return
		}
		if tag.RowsAffected() != 1 {
			fail(w, 400, "Участник группы отсутствует или заблокирован")
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, map[string]string{"id": id})
}
