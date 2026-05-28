package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"testing"
)

func TestTelnetNegotiation_PlainData(t *testing.T) {
	input := []byte("hello world\n")
	conn := &mockTelnetConn{}
	result := handleTelnetNegotiation(conn, input)
	if string(result) != "hello world\n" {
		t.Fatalf("expected 'hello world\\n', got '%s'", string(result))
	}
}

func TestTelnetNegotiation_IACDoDont(t *testing.T) {
	// IAC DO TERMINAL-TYPE → responded with IAC DONT
	// IAC WILL ECHO → responded with IAC WONT
	input := []byte{255, 253, 24, 255, 251, 1}
	conn := &mockTelnetConn{}
	result := handleTelnetNegotiation(conn, input)
	if len(result) != 0 {
		t.Fatalf("expected empty result, got '%s'", string(result))
	}
	if !bytes.Equal(conn.written, []byte{255, 252, 24, 255, 254, 1}) {
		t.Fatalf("expected responses, got %v", conn.written)
	}
}

func TestTelnetNegotiation_IACSubnegotiation(t *testing.T) {
	// IAC SB TERMINAL-TYPE IS "xterm" IAC SE
	input := []byte("before" + string([]byte{255, 250, 24, 0, 'x', 't', 'e', 'r', 'm', 255, 240}) + "after")
	conn := &mockTelnetConn{}
	result := handleTelnetNegotiation(conn, input)
	expected := "beforeafter"
	if string(result) != expected {
		t.Fatalf("expected '%s', got '%s'", expected, string(result))
	}
}

func TestTelnetNegotiation_EscapedIAC(t *testing.T) {
	// IAC IAC → literal 0xFF
	input := []byte("data" + string([]byte{255, 255}) + "end")
	conn := &mockTelnetConn{}
	result := handleTelnetNegotiation(conn, input)
	expected := []byte("data\xffend")
	if !bytes.Equal(result, expected) {
		t.Fatalf("expected %v, got %v", expected, result)
	}
}

func TestTelnetNegotiation_Mixed(t *testing.T) {
	input := []byte("line1\n" + string([]byte{255, 251, 1}) + "line2\n")
	conn := &mockTelnetConn{}
	result := handleTelnetNegotiation(conn, input)
	expected := "line1\nline2\n"
	if string(result) != expected {
		t.Fatalf("expected '%s', got '%s'", expected, string(result))
	}
}

type mockTelnetConn struct {
	net.Conn
	written []byte
}

func (m *mockTelnetConn) Write(b []byte) (int, error) {
	m.written = append(m.written, b...)
	return len(b), nil
}

func TestWaitForPrompt_Basic(t *testing.T) {
	input := "GDB start message\n(gdb) "
	sess := &GdbSession{stdout: bytes.NewReader([]byte(input))}
	ch := make(chan OutputLine, 10)

	err := waitForPrompt(context.Background(), sess, ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	close(ch)
	var lines []string
	for ol := range ch {
		if ol.Type == "stdout" {
			lines = append(lines, ol.Data)
		}
	}

	if len(lines) != 1 || lines[0] != "GDB start message" {
		t.Fatalf("expected ['GDB start message'], got %v", lines)
	}
}

func TestWaitForPrompt_MultiLine(t *testing.T) {
	input := "line1\nline2\n(gdb) "
	sess := &GdbSession{stdout: bytes.NewReader([]byte(input))}
	ch := make(chan OutputLine, 10)

	err := waitForPrompt(context.Background(), sess, ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	close(ch)
	var lines []string
	for ol := range ch {
		if ol.Type == "stdout" {
			lines = append(lines, ol.Data)
		}
	}

	if len(lines) != 2 || lines[0] != "line1" || lines[1] != "line2" {
		t.Fatalf("expected ['line1', 'line2'], got %v", lines)
	}
}

func TestWaitForPrompt_PromptInOutput(t *testing.T) {
	input := "before (gdb) after\n(gdb) "
	sess := &GdbSession{stdout: bytes.NewReader([]byte(input))}
	ch := make(chan OutputLine, 10)

	err := waitForPrompt(context.Background(), sess, ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	close(ch)
	var lines []string
	for ol := range ch {
		if ol.Type == "stdout" {
			lines = append(lines, ol.Data)
		}
	}

	// Note: waitForPrompt detects "(gdb) " suffix on ALL accumulated bytes,
	// so the embedded prompt triggers early. This is a known limitation
	// of the current implementation.
	if len(lines) != 1 || lines[0] != "before " {
		t.Fatalf("expected ['before '], got %v", lines)
	}
}

func TestWaitForPrompt_NoPrompt(t *testing.T) {
	input := "no prompt here\njust more text\n"
	sess := &GdbSession{stdout: bytes.NewReader([]byte(input))}
	ch := make(chan OutputLine, 10)

	err := waitForPrompt(context.Background(), sess, ch)

	if err == nil {
		t.Fatal("expected EOF error, got nil")
	}
}

func TestRequestJSON(t *testing.T) {
	raw := `{"reqId":"1","action":"exec","params":{"cmd":"ls -la"}}`
	var req Request
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if req.ReqId != "1" || req.Action != "exec" {
		t.Fatalf("bad fields: %+v", req)
	}
	cmd, _ := req.Params["cmd"].(string)
	if cmd != "ls -la" {
		t.Fatalf("bad param cmd: '%s'", cmd)
	}
}

func TestResponseJSON(t *testing.T) {
	r := Response{ReqId: "1", Type: "stdout", Data: "hello"}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	expected := `{"reqId":"1","type":"stdout","ok":false,"data":"hello","code":0}`
	if string(data) != expected {
		t.Fatalf("expected '%s', got '%s'", expected, string(data))
	}
}
