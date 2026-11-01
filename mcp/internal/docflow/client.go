package docflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	APIURL       string `json:"api_url"`
	Email        string `json:"email"`
	Password     string `json:"password"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ReadOnly     bool   `json:"read_only"`
}

// Config files may use the existing local-config.json format. Only these fields
// are read; database passwords and application signing secrets are never used.
func LoadConfig(path string) (Config, error) {
	var cfg Config
	if path != "" {
		file, err := os.Open(path)
		if err != nil {
			return cfg, errors.New("не удалось открыть файл конфигурации MCP")
		}
		defer file.Close()
		if json.NewDecoder(io.LimitReader(file, 64<<10)).Decode(&cfg) != nil {
			return cfg, errors.New("некорректный JSON в конфигурации MCP")
		}
	}
	for name, target := range map[string]*string{"DOCFLOW_API_URL": &cfg.APIURL, "DOCFLOW_EMAIL": &cfg.Email, "DOCFLOW_PASSWORD": &cfg.Password, "DOCFLOW_ACCESS_TOKEN": &cfg.AccessToken, "DOCFLOW_REFRESH_TOKEN": &cfg.RefreshToken} {
		if value, ok := os.LookupEnv(name); ok {
			*target = value
		}
	}
	if value, ok := os.LookupEnv("DOCFLOW_MCP_READ_ONLY"); ok {
		v, err := strconv.ParseBool(value)
		if err != nil {
			return cfg, errors.New("DOCFLOW_MCP_READ_ONLY должен быть true или false")
		}
		cfg.ReadOnly = v
	}
	if cfg.APIURL == "" {
		cfg.APIURL = "http://127.0.0.1:8080"
	}
	return cfg, nil
}

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("Docflow HTTP %d: %s", e.Status, e.Message) }

type Client struct {
	base                             string
	http                             *http.Client
	mu                               sync.Mutex
	email, password, access, refresh string
	expires                          time.Time
	ownedSession                     bool
	ReadOnly                         bool
}

func NewClient(cfg Config) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(cfg.APIURL, "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/api/v1") {
		return nil, errors.New("DOCFLOW_API_URL должен указывать на корень API без пароля, параметров и произвольного пути")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "localhost" || (ip != nil && ip.IsLoopback()))) {
		return nil, errors.New("для удалённого Docflow требуется HTTPS; HTTP допускается только на loopback")
	}
	if cfg.AccessToken == "" && cfg.RefreshToken == "" && (cfg.Email == "" || cfg.Password == "") {
		return nil, errors.New("укажите DOCFLOW_EMAIL и DOCFLOW_PASSWORD, токен либо --config-file")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Client{base: strings.TrimSuffix(u.String(), "/api/v1") + "/api/v1", http: &http.Client{Timeout: 45 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, email: cfg.Email, password: cfg.Password, access: cfg.AccessToken, refresh: cfg.RefreshToken, ReadOnly: cfg.ReadOnly}, nil
}

func (c *Client) raw(ctx context.Context, method, path, token string, body any) (any, error) {
	relative, parseErr := url.ParseRequestURI(path)
	if parseErr != nil || relative.IsAbs() || relative.Host != "" || !strings.HasPrefix(relative.Path, "/") || strings.HasPrefix(relative.Path, "//") || strings.Contains(relative.Path, "..") {
		return nil, errors.New("недопустимый путь API")
	}
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return nil, errors.New("не удалось подготовить JSON запроса")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, errors.New("не удалось подготовить запрос Docflow")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "docflow-mcp/1.0")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Docflow недоступен: проверьте адрес API и запущенные службы; изменяющий запрос не повторялся")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil {
		return nil, errors.New("ответ Docflow не удалось прочитать; проверьте состояние операции перед повтором")
	}
	if len(raw) > 8<<20 {
		return nil, errors.New("ответ Docflow превышает 8 МиБ: сузьте запрос")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var api struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &api)
		if api.Error == "" {
			api.Error = http.StatusText(res.StatusCode)
		}
		return nil, &APIError{Status: res.StatusCode, Message: c.redact(api.Error)}
	}
	if res.StatusCode == 204 || len(raw) == 0 {
		return map[string]any{"ok": true}, nil
	}
	var result any
	if json.Unmarshal(raw, &result) != nil {
		return nil, errors.New("API Docflow вернул некорректный JSON")
	}
	return result, nil
}

func (c *Client) redact(text string) string {
	for _, secret := range []string{c.password, c.access, c.refresh} {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
	}
	return text
}

func (c *Client) authenticate(ctx context.Context) error {
	var data any
	var err error
	if c.refresh != "" {
		data, err = c.raw(ctx, "POST", "/auth/refresh", "", map[string]string{"refresh_token": c.refresh})
		var api *APIError
		if err != nil && (!errors.As(err, &api) || api.Status != 401) {
			return err
		}
	}
	if data == nil {
		if c.email == "" || c.password == "" {
			return errors.New("сессия истекла: подключите MCP заново с действующей учётной записью")
		}
		data, err = c.raw(ctx, "POST", "/auth/login", "", map[string]string{"email": c.email, "password": c.password})
		if err != nil {
			return err
		}
		c.ownedSession = true
	}
	session, ok := data.(map[string]any)
	if !ok {
		return errors.New("некорректный ответ авторизации")
	}
	access, _ := session["access_token"].(string)
	refresh, _ := session["refresh_token"].(string)
	seconds, _ := session["expires_in"].(float64)
	if access == "" || refresh == "" || seconds <= 0 {
		return errors.New("API не выдал полноценную пользовательскую сессию")
	}
	c.access, c.refresh = access, refresh
	c.expires = time.Now().Add(time.Duration(seconds) * time.Second)
	return nil
}

func (c *Client) Do(ctx context.Context, method, path string, body any) (any, error) {
	if c.ReadOnly && method != "GET" {
		return nil, errors.New("MCP работает в режиме только чтения")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.access == "" || (!c.expires.IsZero() && time.Until(c.expires) < 30*time.Second) {
		if err := c.authenticate(ctx); err != nil {
			return nil, err
		}
	}
	data, err := c.raw(ctx, method, path, c.access, body)
	var api *APIError
	// Only a definitive authentication rejection is replayed, never a timeout or
	// a business error. Refresh rotation is serialized across concurrent tools.
	if errors.As(err, &api) && api.Status == 401 && (c.refresh != "" || c.email != "") {
		if err = c.authenticate(ctx); err != nil {
			return nil, err
		}
		return c.raw(ctx, method, path, c.access, body)
	}
	return data, err
}

func (c *Client) Close(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ownedSession && c.refresh != "" {
		_, _ = c.raw(ctx, "POST", "/auth/logout", "", map[string]string{"refresh_token": c.refresh})
		c.refresh = ""
		c.access = ""
	}
}
