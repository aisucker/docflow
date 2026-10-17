package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

type claims struct {
	UID string `json:"uid"`
	Exp int64  `json:"exp"`
}

func (s *Server) makeAccess(id string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	data, _ := json.Marshal(claims{UID: id, Exp: time.Now().Add(15 * time.Minute).Unix()})
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Server) verifyAccess(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("invalid token")
	}
	if parts[0] != base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) {
		return "", errors.New("invalid header")
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	sig, e := base64.RawURLEncoding.DecodeString(parts[2])
	if e != nil || !hmac.Equal(mac.Sum(nil), sig) {
		return "", errors.New("invalid signature")
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return "", e
	}
	var c claims
	if e = json.Unmarshal(b, &c); e != nil || c.UID == "" || time.Now().Unix() >= c.Exp {
		return "", errors.New("expired")
	}
	return c.UID, nil
}
func (s *Server) authorize(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if header == "" {
			fail(w, 401, "Требуется авторизация")
			return
		}
		var p Principal
		if uid, err := s.verifyAccess(header); err == nil {
			err = s.db.QueryRow(r.Context(), "SELECT id,email,full_name,role FROM users WHERE id=$1 AND active=true", uid).Scan(&p.ID, &p.Email, &p.Name, &p.Role)
			if err == nil {
				handler(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, p)))
				return
			}
		}
		if strings.HasPrefix(header, "dfi_") && (strings.HasPrefix(r.URL.Path, "/api/v1/documents") || r.URL.Path == "/api/v1/document-types" || strings.HasPrefix(r.URL.Path, "/api/v1/files")) {
			if p, err := s.integrationPrincipal(r.Context(), header); err == nil {
				handler(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, p)))
				return
			}
		}
		fail(w, 401, "Недействительный или истёкший токен")
	}
}
func (s *Server) issueSession(ctx context.Context, userID string) (map[string]any, error) {
	refresh := randomToken()
	_, err := s.db.Exec(ctx, "INSERT INTO refresh_sessions(user_id,token_hash,expires_at) VALUES ($1,$2,now()+interval '14 days')", userID, tokenHash(refresh))
	if err != nil {
		return nil, err
	}
	return map[string]any{"access_token": s.makeAccess(userID), "refresh_token": refresh, "token_type": "Bearer", "expires_in": 900}, nil
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	var id, hash string
	err := s.db.QueryRow(r.Context(), "SELECT id,password_hash FROM users WHERE email=lower($1) AND active=true", strings.TrimSpace(in.Email)).Scan(&id, &hash)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil {
		fail(w, 401, "Неверный логин или пароль")
		return
	}
	result, err := s.issueSession(r.Context(), id)
	if err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, result)
}
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"refresh_token"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Token) != 64 {
		fail(w, 401, "Недействительный токен")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		errDB(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var sessionID, userID string
	err = tx.QueryRow(r.Context(), `SELECT rs.id,rs.user_id FROM refresh_sessions rs JOIN users u ON u.id=rs.user_id WHERE rs.token_hash=$1 AND rs.expires_at>now() AND rs.revoked_at IS NULL AND u.active=true FOR UPDATE OF rs`, tokenHash(in.Token)).Scan(&sessionID, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 401, "Недействительный или отозванный токен")
		return
	}
	if err != nil {
		errDB(w, err)
		return
	}
	_, err = tx.Exec(r.Context(), "UPDATE refresh_sessions SET revoked_at=now() WHERE id=$1", sessionID)
	if err != nil {
		errDB(w, err)
		return
	}
	refresh := randomToken()
	_, err = tx.Exec(r.Context(), "INSERT INTO refresh_sessions(user_id,token_hash,expires_at) VALUES($1,$2,now()+interval '14 days')", userID, tokenHash(refresh))
	if err != nil {
		errDB(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		errDB(w, err)
		return
	}
	respond(w, 200, map[string]any{"access_token": s.makeAccess(userID), "refresh_token": refresh, "token_type": "Bearer", "expires_in": 900})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"refresh_token"`
	}
	if !decode(w, r, &in) {
		return
	}
	_, _ = s.db.Exec(r.Context(), "UPDATE refresh_sessions SET revoked_at=now() WHERE token_hash=$1", tokenHash(in.Token))
	w.WriteHeader(204)
}
