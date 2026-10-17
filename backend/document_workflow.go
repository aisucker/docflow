package main

import (
	"encoding/json"
	"net/http"
)

func (s *Server) documentWorkflow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.readAllowed(r.Context(), id, me(r)) {
		fail(w, 404, "Документ не найден")
		return
	}
	var definition json.RawMessage
	var key, name string
	var version int
	err := s.db.QueryRow(r.Context(), `SELECT wd.workflow_key,wd.name,wd.version,wi.definition_snapshot
 FROM workflow_instances wi JOIN workflow_definitions wd ON wd.id=wi.definition_id
 WHERE wi.document_id=$1 ORDER BY wi.started_at DESC,wi.id DESC LIMIT 1`, id).Scan(&key, &name, &version, &definition)
	if err != nil {
		err = s.db.QueryRow(r.Context(), `SELECT wd.workflow_key,wd.name,wd.version,wd.definition
 FROM documents d JOIN document_types dt ON dt.id=d.type_id JOIN workflow_definitions wd ON wd.workflow_key=coalesce(d.workflow_key,dt.default_workflow_key) AND wd.active=true
 WHERE d.id=$1 ORDER BY wd.version DESC LIMIT 1`, id).Scan(&key, &name, &version, &definition)
		if err != nil {
			respond(w, 200, map[string]any{"configured": false})
			return
		}
	}
	respond(w, 200, map[string]any{"configured": true, "workflow_key": key, "name": name, "version": version, "definition": definition})
}
