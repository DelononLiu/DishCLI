package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStripANSI_Plain(t *testing.T) {
	input := "hello world"
	result := stripANSI(input)
	if result != input {
		t.Fatalf("expected '%s', got '%s'", input, result)
	}
}

func TestStripANSI_ColorCodes(t *testing.T) {
	input := "\x1b[31mred\x1b[0m normal"
	expected := "red normal"
	result := stripANSI(input)
	if result != expected {
		t.Fatalf("expected '%s', got '%s'", expected, result)
	}
}

func TestStripANSI_MultipleColors(t *testing.T) {
	input := "\x1b[32mgreen\x1b[33m yellow\x1b[0m"
	expected := "green yellow"
	result := stripANSI(input)
	if result != expected {
		t.Fatalf("expected '%s', got '%s'", expected, result)
	}
}

func TestStripANSI_Bold(t *testing.T) {
	input := "\x1b[1mbold\x1b[22m"
	expected := "bold"
	result := stripANSI(input)
	if result != expected {
		t.Fatalf("expected '%s', got '%s'", expected, result)
	}
}

func TestStripANSI_CursorMove(t *testing.T) {
	input := "line1\x1b[Aline2"
	expected := "line1line2"
	result := stripANSI(input)
	if result != expected {
		t.Fatalf("expected '%s', got '%s'", expected, result)
	}
}

func TestStripANSI_ClearScreen(t *testing.T) {
	input := "\x1b[2J\x1b[Hhello"
	expected := "hello"
	result := stripANSI(input)
	if result != expected {
		t.Fatalf("expected '%s', got '%s'", expected, result)
	}
}

func TestStripANSI_OSCSequence(t *testing.T) {
	input := "\x1b]0;window title\x1b\\hello"
	expected := "hello"
	result := stripANSI(input)
	if result != expected {
		t.Fatalf("expected '%s', got '%s'", expected, result)
	}
}

func TestStripANSI_OSCWithBell(t *testing.T) {
	input := "\x1b]0;window title\x07hello"
	expected := "hello"
	result := stripANSI(input)
	if result != expected {
		t.Fatalf("expected '%s', got '%s'", expected, result)
	}
}

func TestStripANSI_ControlChars(t *testing.T) {
	input := "\x00\x01\x02hello\x7f"
	expected := "hello"
	result := stripANSI(input)
	if result != expected {
		t.Fatalf("expected '%s', got '%s'", expected, result)
	}
}

func TestStripANSI_Complex(t *testing.T) {
	input := "\x1b[38;5;82mcolored\x1b[0m \x1b[1m\x1b[31mbold red\x1b[0m"
	expected := "colored bold red"
	result := stripANSI(input)
	if result != expected {
		t.Fatalf("expected '%s', got '%s'", expected, result)
	}
}

func TestStripANSI_Empty(t *testing.T) {
	result := stripANSI("")
	if result != "" {
		t.Fatalf("expected empty, got '%s'", result)
	}
}

func TestResponsePTYFormat(t *testing.T) {
	resp := Response{
		ReqId: "1",
		Type:  "stdout",
		Raw:   "\x1b[32mhello\x1b[0m",
		Text:  "hello",
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var parsed map[string]interface{}
	json.Unmarshal(data, &parsed)

	if _, hasData := parsed["data"]; hasData {
		t.Fatal("PTY response should not have 'data' field")
	}
	if _, hasRaw := parsed["raw"]; !hasRaw {
		t.Fatal("PTY response should have 'raw' field")
	}
	if _, hasText := parsed["text"]; !hasText {
		t.Fatal("PTY response should have 'text' field")
	}
	if parsed["raw"] != "\x1b[32mhello\x1b[0m" {
		t.Fatalf("unexpected raw: %v", parsed["raw"])
	}
	if parsed["text"] != "hello" {
		t.Fatalf("unexpected text: %v", parsed["text"])
	}
}

func TestResponseNoPTYFormat(t *testing.T) {
	resp := Response{
		ReqId: "1",
		Type:  "stdout",
		Data:  "hello",
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var parsed map[string]interface{}
	json.Unmarshal(data, &parsed)

	if _, hasData := parsed["data"]; !hasData {
		t.Fatal("non-PTY response should have 'data' field")
	}
	if _, hasRaw := parsed["raw"]; hasRaw {
		t.Fatal("non-PTY response should not have 'raw' field")
	}
	if _, hasText := parsed["text"]; hasText {
		t.Fatal("non-PTY response should not have 'text' field")
	}
	if parsed["data"] != "hello" {
		t.Fatalf("unexpected data: %v", parsed["data"])
	}
}

func runLocalExecPTY(ctx context.Context, cmd string) <-chan OutputLine {
	ch := make(chan OutputLine)
	go func() {
		defer close(ch)
		localExecPTY(ctx, cmd, ch)
	}()
	return ch
}

func TestLocalExecPTY_Echo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	ch := runLocalExecPTY(ctx, "echo hello")

	var stdout string
	var exitCode int
	for ol := range ch {
		switch ol.Type {
		case "stdout":
			stdout += ol.Text
			if ol.Raw == "" {
				t.Fatal("PTY output should have non-empty Raw")
			}
			if ol.Text == "" {
				t.Fatal("PTY output should have non-empty Text")
			}
		case "exit":
			exitCode = ol.Code
		case "error":
			t.Fatalf("unexpected error: %s", ol.Msg)
		}
	}

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	trimmed := strings.TrimSpace(stdout)
	if !strings.Contains(trimmed, "hello") {
		t.Fatalf("expected stdout to contain 'hello', got '%s'", stdout)
	}
}

func TestLocalExecPTY_ExitCode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	ch := runLocalExecPTY(ctx, "exit 42")

	var exitCode int
	for ol := range ch {
		switch ol.Type {
		case "exit":
			exitCode = ol.Code
		case "error":
			t.Fatalf("unexpected error: %s", ol.Msg)
		}
	}

	if exitCode != 42 {
		t.Fatalf("expected exit code 42, got %d", exitCode)
	}
}

func TestLocalExecPTY_MultiLineOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	ch := runLocalExecPTY(ctx, "echo -e 'line1\\nline2\\nline3'")

	var lines []string
	for ol := range ch {
		switch ol.Type {
		case "stdout":
			lines = append(lines, ol.Text)
		case "exit":
		case "error":
			t.Fatalf("unexpected error: %s", ol.Msg)
		}
	}

	full := strings.Join(lines, "")
	if !strings.Contains(full, "line1") || !strings.Contains(full, "line2") || !strings.Contains(full, "line3") {
		t.Fatalf("expected all lines in output, got '%s'", full)
	}
}

func TestLocalExecPTY_Timeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	ch := runLocalExecPTY(ctx, "sleep 10")

	var gotTimeout bool
	for ol := range ch {
		if ol.Type == "error" && strings.Contains(ol.Msg, "timeout") {
			gotTimeout = true
		}
	}

	if !gotTimeout {
		t.Fatal("expected timeout error")
	}
}

func TestLocalExecPTY_WithColors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	ch := runLocalExecPTY(ctx, `echo -e '\033[31mred\033[0m'`)

	for ol := range ch {
		switch ol.Type {
		case "stdout":
			if ol.Raw == "" {
				t.Fatal("PTY stdout should have raw field")
			}
			if !strings.Contains(ol.Raw, "\x1b[") && !strings.Contains(ol.Raw, "\033[") {
				t.Logf("warning: raw output doesn't contain ANSI codes (colors might be disabled in this env): %q", ol.Raw)
			}
			if ol.Text == "" {
				t.Fatal("PTY stdout should have text field")
			}
		case "exit":
		case "error":
			t.Fatalf("unexpected error: %s", ol.Msg)
		}
	}
}

func TestLocalExecPTY_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ch := runLocalExecPTY(ctx, "echo should not run")

	var gotExit bool
	for ol := range ch {
		switch ol.Type {
		case "exit":
			gotExit = true
		case "error":
		}
	}

	if gotExit {
		t.Fatal("should not get exit on cancelled context")
	}
}

func TestStripANSI_SGRReset(t *testing.T) {
	input := "\x1b[mreset\x1b[0m"
	expected := "reset"
	result := stripANSI(input)
	if result != expected {
		t.Fatalf("expected '%s', got '%s'", expected, result)
	}
}

func TestStripANSI_NewlinePreserved(t *testing.T) {
	input := "\x1b[32mhello\x1b[0m\n\x1b[31mworld\x1b[0m"
	expected := "hello\nworld"
	result := stripANSI(input)
	if result != expected {
		t.Fatalf("expected '%q', got '%q'", expected, result)
	}
}

func TestResponsePTY_HandleExecAction(t *testing.T) {
	// Set up local client
	mu.Lock()
	client = &Client{typ: ConnLocal}
	mu.Unlock()
	defer func() {
		mu.Lock()
		client = nil
		mu.Unlock()
	}()

	// Capture output via pipe
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe failed: %v", err)
	}
	oldStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = oldStdout }()

	req := Request{
		ReqId:  "1",
		Action: "exec",
		Params: map[string]interface{}{
			"cmd": "echo hello",
			"pty": true,
		},
	}
	HandleExecAction(req)

	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	r.Close()

	scanner := bufio.NewScanner(&buf)
	var foundStdout bool
	for scanner.Scan() {
		line := scanner.Text()
		var resp Response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			continue
		}
		if resp.Type == "stdout" {
			foundStdout = true
			if resp.Raw == "" {
				t.Fatal("PTY exec stdout should have Raw field")
			}
			if resp.Text == "" {
				t.Fatal("PTY exec stdout should have Text field")
			}
			if resp.Data != "" {
				t.Fatal("PTY exec stdout should not have Data field")
			}
		}
	}
	if !foundStdout {
		t.Fatal("expected stdout output")
	}
}
