package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Request struct {
	ReqId  string                 `json:"reqId"`
	Action string                 `json:"action"`
	Params map[string]interface{} `json:"params"`
}

type Response struct {
	ReqId string `json:"reqId"`
	Type  string `json:"type"`
	Ok    bool   `json:"ok"`
	Data  string `json:"data,omitempty"`
	Code  int    `json:"code"`
	Msg   string `json:"msg,omitempty"`
}

type OutputLine struct {
	Type string
	Data string
	Code int
	Msg  string
	Ok   bool
}

type Executor interface {
	Exec(ctx context.Context, action string, params map[string]interface{}) <-chan OutputLine
}

var defaultTimeout = 30 * time.Second
var stdoutMu sync.Mutex

func writeResp(resp Response) {
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	w := bufio.NewWriter(os.Stdout)
	data, _ := json.Marshal(resp)
	w.Write(data)
	w.WriteByte('\n')
	w.Flush()
}

func writeResult(reqId string, ok bool) {
	writeResp(Response{ReqId: reqId, Type: "result", Ok: ok})
}

func writeStdout(reqId, data string) {
	writeResp(Response{ReqId: reqId, Type: "stdout", Data: data})
}

func writeStderr(reqId, data string) {
	writeResp(Response{ReqId: reqId, Type: "stderr", Data: data})
}

func writeExit(reqId string, code int) {
	writeResp(Response{ReqId: reqId, Type: "exit", Code: code})
}

func writeError(reqId, msg string) {
	writeResp(Response{ReqId: reqId, Type: "error", Msg: msg})
}

func writeShellOutput(data string) {
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	resp, _ := json.Marshal(Response{
		ReqId: "_shell_output",
		Type:  "stdout",
		Data:  data,
	})
	os.Stdout.Write(resp)
	os.Stdout.Write([]byte{'\n'})
}

func writeShellStderr(data string) {
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	resp, _ := json.Marshal(Response{
		ReqId: "_shell_output",
		Type:  "stderr",
		Data:  data,
	})
	os.Stdout.Write(resp)
	os.Stdout.Write([]byte{'\n'})
}

func HandleExecAction(req Request) {
	mu.Lock()
	if client == nil {
		mu.Unlock()
		writeError(req.ReqId, "not connected")
		return
	}
	c := client
	mu.Unlock()

	var exec Executor
	switch req.Action {
	case "exec":
		exec = &OneShotExecutor{}
	case "gdbStart", "gdbCmd", "gdbExit":
		exec = &InteractiveExecutor{clientTyp: c.typ}
	default:
		writeError(req.ReqId, "unknown action")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	ch := exec.Exec(ctx, req.Action, req.Params)
	for ol := range ch {
		resp := Response{
			ReqId: req.ReqId,
			Type:  ol.Type,
			Ok:    ol.Ok,
			Data:  ol.Data,
			Code:  ol.Code,
			Msg:   ol.Msg,
		}
		writeResp(resp)
	}
}

func HandleShellStart(req Request) {
	shellSessMu.Lock()
	defer shellSessMu.Unlock()

	if activeShell != nil {
		killShellLocked()
	}

	params := req.Params
	shell, _ := params["shell"].(string)
	if shell == "" {
		shell = "bash"
	}

	cwd, _ := params["cwd"].(string)
	sess := newShellSession(shell, cwd)
	if sess == nil {
		writeResp(Response{ReqId: req.ReqId, Type: "error", Msg: "failed to start shell"})
		return
	}

	activeShell = sess
	writeResp(Response{ReqId: req.ReqId, Type: "result", Ok: true})
	writeResp(Response{ReqId: req.ReqId, Type: "exit", Code: 0})
}

func HandleShellWrite(req Request) {
	shellSessMu.Lock()
	sess := activeShell
	shellSessMu.Unlock()

	if sess == nil {
		writeResp(Response{ReqId: req.ReqId, Type: "error", Msg: "no active shell"})
		return
	}

	data, _ := req.Params["data"].(string)
	if data == "" {
		writeResp(Response{ReqId: req.ReqId, Type: "error", Msg: "empty data"})
		return
	}

	if err := sess.write(data); err != nil {
		writeResp(Response{ReqId: req.ReqId, Type: "error", Msg: err.Error()})
		return
	}

	writeResp(Response{ReqId: req.ReqId, Type: "result", Ok: true})
	writeResp(Response{ReqId: req.ReqId, Type: "exit", Code: 0})
}

func HandleShellStop(req Request) {
	shellSessMu.Lock()
	killShellLocked()
	shellSessMu.Unlock()

	writeResp(Response{ReqId: req.ReqId, Type: "result", Ok: true})
	writeResp(Response{ReqId: req.ReqId, Type: "exit", Code: 0})
}

var (
	activeShell *ShellSession
	shellSessMu sync.Mutex
)

type ShellSession struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	cancel  context.CancelFunc
	started bool
}

func newShellSession(shell string, cwd string) *ShellSession {
	ctx, cancel := context.WithCancel(context.Background())

	args := []string{"--norc", "--noprofile", "-i"}

	cmd := exec.CommandContext(ctx, shell, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil
	}

	sess := &ShellSession{
		cmd:     cmd,
		stdin:   stdin,
		cancel:  cancel,
		started: true,
	}

	go func() {
		buf := make([]byte, 4096)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			n, err := stdout.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				writeShellOutput(chunk)
			}
			if err != nil {
				return
			}
		}
	}()

	go func() {
		buf := make([]byte, 4096)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			n, err := stderr.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				writeShellStderr(chunk)
			}
			if err != nil {
				return
			}
		}
	}()

	return sess
}

func (s *ShellSession) write(data string) error {
	if !s.started {
		return fmt.Errorf("shell not started")
	}
	data = strings.ReplaceAll(data, "\r", "\n")
	_, err := fmt.Fprint(s.stdin, data)
	return err
}

func (s *ShellSession) close() {
	s.cancel()
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
}

func killShellLocked() {
	if activeShell != nil {
		activeShell.close()
		activeShell = nil
	}
}
