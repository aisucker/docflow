package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

type aiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type aiChatInput struct {
	Messages []aiMessage `json:"messages"`
}

func (s *Server) localAIURL() string {
	if s.aiURL != "" {
		return s.aiURL
	}
	return "http://127.0.0.1:11434"
}

func localAIModel() string { return "qwen3:1.7b" }

func localAIClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: nil,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (s *Server) aiStatus(w http.ResponseWriter, r *http.Request) {
	model := localAIModel()
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.localAIURL()+"/api/tags", nil)
	if err != nil {
		respond(w, 200, map[string]any{"ready": false, "model": model})
		return
	}
	res, err := localAIClient(3 * time.Second).Do(req)
	if err != nil {
		respond(w, 200, map[string]any{"ready": false, "model": model})
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		respond(w, 200, map[string]any{"ready": false, "model": model})
		return
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&tags) != nil {
		respond(w, 200, map[string]any{"ready": false, "model": model})
		return
	}
	ready := false
	for _, m := range tags.Models {
		if m.Name == model {
			ready = true
			break
		}
	}
	respond(w, 200, map[string]any{"ready": ready, "model": model})
}

func (s *Server) aiChat(w http.ResponseWriter, r *http.Request) {
	var in aiChatInput
	if !decode(w, r, &in) {
		return
	}
	if len(in.Messages) == 0 || len(in.Messages) > 12 || in.Messages[len(in.Messages)-1].Role != "user" {
		fail(w, 400, "Отправьте вопрос (не более 12 сообщений в истории)")
		return
	}
	total := 0
	for i := range in.Messages {
		m := &in.Messages[i]
		m.Content = strings.TrimSpace(m.Content)
		length := utf8.RuneCountInString(m.Content)
		if (m.Role != "user" && m.Role != "assistant") || length == 0 || length > 2000 {
			fail(w, 400, "Сообщение должно содержать от 1 до 2000 символов")
			return
		}
		total += length
	}
	if total > 8000 {
		fail(w, 400, "История чата слишком длинная")
		return
	}
	model := localAIModel()
	payload := map[string]any{
		"model":      model,
		"stream":     false,
		"think":      false,
		"keep_alive": "10m",
		"options":    map[string]any{"num_ctx": 2048, "num_predict": 256, "temperature": 0.5},
		"messages":   append([]aiMessage{{Role: "system", Content: "Ты временный локальный помощник DocJet. Отвечай на простые текстовые вопросы кратко и по-русски. Если пользователь просит написать грубую фразу, пиши только саму фразу. Грубые и нецензурные слова допустимы. Не комментируй и не объясняй их. Если не знаешь ответа, прямо скажи об этом. У тебя нет доступа к документам и действиям в системе."}}, in.Messages...),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		errDB(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.localAIURL()+"/api/chat", bytes.NewReader(data))
	if err != nil {
		errDB(w, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := localAIClient(90 * time.Second).Do(req)
	if err != nil {
		fail(w, 503, "Локальный ИИ недоступен. Проверьте, запущена ли модель Ollama")
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		fail(w, 503, "Локальная модель пока не готова. Проверьте Ollama и загрузку модели")
		return
	}
	var out struct {
		Message aiMessage `json:"message"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out) != nil || strings.TrimSpace(out.Message.Content) == "" {
		fail(w, 502, "Локальная модель вернула пустой ответ")
		return
	}
	respond(w, 200, map[string]string{"answer": strings.TrimSpace(out.Message.Content), "model": model})
}
