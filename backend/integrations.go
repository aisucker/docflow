package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	rows, err := s.db.Query(r.Context(), "SELECT id,name,url,active,created_at FROM webhooks ORDER BY created_at DESC")
	if err != nil {
		errDB(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, u string
		var active bool
		var created time.Time
		if e := rows.Scan(&id, &name, &u, &active, &created); e != nil {
			errDB(w, e)
			return
		}
		out = append(out, map[string]any{"id": id, "name": name, "url": u, "active": active, "created_at": created})
	}
	respond(w, 200, out)
}
func goodWebhookURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" {
		return false
	}
	return !strings.EqualFold(u.Hostname(), "localhost")
}
func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	var in struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Name == "" || !goodWebhookURL(in.URL) {
		fail(w, 400, "Нужны название и публичный HTTPS URL")
		return
	}
	secret := randomToken()
	var id string
	err := s.db.QueryRow(r.Context(), "INSERT INTO webhooks(name,url,secret,created_by) VALUES($1,$2,$3,$4) RETURNING id", in.Name, in.URL, secret, me(r).ID).Scan(&id)
	if err != nil {
		errDB(w, err)
		return
	}
	respond(w, 201, map[string]string{"id": id, "signing_secret": secret})
}
func (s *Server) createIntegrationToken(w http.ResponseWriter, r *http.Request) {
	if !mustRole(w, me(r), "admin") {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		fail(w, 400, "Укажите название интеграции")
		return
	}
	token := "dfi_" + randomToken()
	var id string
	err := s.db.QueryRow(r.Context(), "INSERT INTO integration_tokens(name,token_hash,created_by) VALUES($1,$2,$3) RETURNING id", in.Name, tokenHash(token), me(r).ID).Scan(&id)
	if err != nil {
		errDB(w, err)
		return
	}
	respond(w, 201, map[string]string{"id": id, "token": token, "scope": "documents:read_write"})
}
func (s *Server) integrationPrincipal(ctx context.Context, token string) (Principal, error) {
	var p Principal
	var name string
	err := s.db.QueryRow(ctx, `SELECT u.id,u.email,u.full_name,t.name FROM integration_tokens t JOIN users u ON u.id=t.created_by WHERE t.token_hash=$1 AND t.active=true AND u.active=true`, tokenHash(token)).Scan(&p.ID, &p.Email, &p.Name, &name)
	if err != nil {
		return p, err
	}
	p.Name = "Integration: " + name
	p.Role = "registrar"
	return p, nil
}
func publicIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast()
}
func safeDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if !publicIP(ip.IP) {
			return nil, errors.New("webhook IP is not public")
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("no public IP")
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", net.JoinHostPort(ips[0].IP.String(), port))
}
func (s *Server) webhookWorker(ctx context.Context) {
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{DialContext: safeDial, MaxIdleConnsPerHost: 2, IdleConnTimeout: 20 * time.Second}}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.deliverOutbox(ctx, client)
		}
	}
}
func (s *Server) deliverOutbox(ctx context.Context, client *http.Client) {
	rows, err := s.db.Query(ctx, "SELECT id,event_type,payload,attempts FROM outbox WHERE delivered_at IS NULL AND next_attempt_at <= now() ORDER BY id LIMIT 20")
	if err != nil {
		log.Printf("outbox: %v", err)
		return
	}
	type item struct {
		ID       int64
		Kind     string
		Payload  []byte
		Attempts int
	}
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.ID, &v.Kind, &v.Payload, &v.Attempts); err == nil {
			items = append(items, v)
		}
	}
	rows.Close()
	for _, v := range items {
		hookRows, e := s.db.Query(ctx, "SELECT url,secret FROM webhooks WHERE active=true")
		if e != nil {
			return
		}
		hooks := [][2]string{}
		for hookRows.Next() {
			var u, sec string
			if hookRows.Scan(&u, &sec) == nil {
				hooks = append(hooks, [2]string{u, sec})
			}
		}
		hookRows.Close()
		ok := true
		body, _ := json.Marshal(map[string]any{"id": v.ID, "event": v.Kind, "data": json.RawMessage(v.Payload)})
		for _, hook := range hooks {
			if !goodWebhookURL(hook[0]) {
				ok = false
				continue
			}
			mac := hmac.New(sha256.New, []byte(hook[1]))
			mac.Write(body)
			request, e := http.NewRequestWithContext(ctx, "POST", hook[0], strings.NewReader(string(body)))
			if e != nil {
				ok = false
				continue
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Docflow-Event", v.Kind)
			request.Header.Set("X-Docflow-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
			response, e := client.Do(request)
			if e != nil {
				ok = false
				continue
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 2048))
			_ = response.Body.Close()
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				ok = false
			}
		}
		if ok {
			_, _ = s.db.Exec(ctx, "UPDATE outbox SET delivered_at=now() WHERE id=$1", v.ID)
		} else {
			delay := 1 << min(v.Attempts, 8)
			_, _ = s.db.Exec(ctx, "UPDATE outbox SET attempts=attempts+1,next_attempt_at=now()+($2 || ' seconds')::interval WHERE id=$1", v.ID, fmt.Sprint(delay*10))
		}
	}
}
