package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

const maxTagValue = 64 << 10

var placeholderSlots = make(chan struct{}, 2)

type placeholderJob struct {
	Script   string          `json:"script"`
	Document json.RawMessage `json:"document"`
}
type placeholderResult struct {
	Value string `json:"value"`
	Error string `json:"error,omitempty"`
}

// Only JSON data crosses the process boundary. No Go functions, filesystem,
// network, require(), process or application secrets are exposed to JavaScript.
func runPlaceholderScript(job placeholderJob) placeholderResult {
	if len(job.Script) > 16<<10 || len(job.Document) > 2<<20 {
		return placeholderResult{Error: "Слишком большой скрипт или документ"}
	}
	vm := goja.New()
	vm.SetMaxCallStackSize(256)
	timer := time.AfterFunc(200*time.Millisecond, func() { vm.Interrupt("Превышено время выполнения JavaScript (200 мс)") })
	defer timer.Stop()
	raw, _ := json.Marshal(string(job.Document))
	source := `"use strict"; (function(){
 const document=JSON.parse(` + string(raw) + `);
 function freeze(value){if(value && typeof value==='object'){Object.keys(value).forEach(k=>freeze(value[k]));Object.freeze(value)}return value}
 freeze(document);
 const helpers=Object.freeze({
  date:function(value){if(!value)return '';var d=new Date(value);if(isNaN(d.getTime()))return '';d=new Date(d.getTime()+5*3600000);return ('0'+d.getUTCDate()).slice(-2)+'.'+('0'+(d.getUTCMonth()+1)).slice(-2)+'.'+d.getUTCFullYear()},
  text:function(value){if(value===null||value===undefined)return '';if(Array.isArray(value))return value.map(helpers.text).join(', ');if(typeof value==='object')return JSON.stringify(value);return String(value)}
 });
 const value=(function(document,helpers){` + job.Script + `
 })(document,helpers);
 if(value && typeof value.then==='function')throw new Error('Верните текст синхронно, без Promise');
 if(value!==null && typeof value==='object')throw new Error('Верните текст, число или boolean. Используйте helpers.text для массива/объекта');
 return value===undefined||value===null?'':String(value);
 })()`
	value, err := vm.RunString(source)
	if err != nil {
		message := err.Error()
		if len(message) > 1500 {
			message = message[:1500]
		}
		return placeholderResult{Error: message}
	}
	result := value.String()
	for _, c := range result {
		if (c < 32 && c != 9 && c != 10 && c != 13) || c == 0xfffe || c == 0xffff {
			return placeholderResult{Error: "Результат содержит недопустимые для DOCX символы"}
		}
	}
	if len(result) > maxTagValue {
		return placeholderResult{Error: "Результат тега превышает 64 КБ"}
	}
	return placeholderResult{Value: result}
}
func placeholderWorkerMain() {
	if err := limitWorkerMemory(); err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(placeholderResult{Error: "Не удалось ограничить память JavaScript"})
		return
	}
	var job placeholderJob
	if json.NewDecoder(io.LimitReader(os.Stdin, 3<<20)).Decode(&job) != nil {
		_ = json.NewEncoder(os.Stdout).Encode(placeholderResult{Error: "Неверный запрос JavaScript"})
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode(runPlaceholderScript(job))
}

type limitedOutput struct {
	bytes.Buffer
	mu sync.Mutex
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Len()+len(p) > 256<<10 {
		return 0, fmt.Errorf("output limit")
	}
	return b.Buffer.Write(p)
}
func evaluatePlaceholder(ctx context.Context, script string, document json.RawMessage) (string, error) {
	select {
	case placeholderSlots <- struct{}{}:
		defer func() { <-placeholderSlots }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	args := []string{"--placeholder-worker"}
	if strings.HasSuffix(strings.ToLower(exe), ".test.exe") || strings.HasSuffix(exe, ".test") {
		args = []string{"-test.run=^TestPlaceholderWorkerProcess$"}
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	hideOfficeProcess(cmd)
	cmd.Env = []string{"DOCJET_PLACEHOLDER_WORKER=1", "GOMEMLIMIT=96MiB", "GOGC=50", "SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), "WINDIR=" + os.Getenv("WINDIR")}
	var stdout, stderr limitedOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	if err = cmd.Start(); err != nil {
		return "", err
	}
	release, err := attachWorkerLimit(cmd.Process)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", fmt.Errorf("Не удалось изолировать JavaScript")
	}
	defer release()
	input, _ := json.Marshal(placeholderJob{Script: script, Document: document})
	_, writeErr := stdin.Write(input)
	_ = stdin.Close()
	err = cmd.Wait()
	if ctx.Err() != nil {
		return "", fmt.Errorf("JavaScript превысил время выполнения")
	}
	if err != nil || writeErr != nil {
		return "", fmt.Errorf("JavaScript остановлен: превышен лимит памяти или произошла ошибка выполнения")
	}
	var result placeholderResult
	if json.Unmarshal(stdout.Bytes(), &result) != nil {
		return "", fmt.Errorf("Некорректный ответ JavaScript")
	}
	if result.Error != "" {
		return "", fmt.Errorf("%s", result.Error)
	}
	return result.Value, nil
}
