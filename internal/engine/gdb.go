package engine

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const gdbPrompt = "(gdb) "

type GdbSession struct {
	mu         sync.Mutex
	clientTyp  ConnType
	stdin      io.WriteCloser
	stdout     io.Reader
	stderr     io.Reader
	sshSession *ssh.Session
	localCmd   *exec.Cmd
	promptBuf  bytes.Buffer
}

var (
	activeSession *GdbSession
	sessionMu     sync.Mutex
)

type InteractiveExecutor struct {
	clientTyp ConnType
}

func (e *InteractiveExecutor) Exec(ctx context.Context, action string, params map[string]interface{}) <-chan OutputLine {
	ch := make(chan OutputLine)
	go func() {
		defer close(ch)
		switch action {
		case "gdbStart":
			e.start(ctx, params, ch)
		case "gdbCmd":
			e.cmd(ctx, params, ch)
		case "gdbExit":
			e.exit(ctx, ch)
		default:
			ch <- OutputLine{Type: "error", Msg: "unknown interactive action: " + action}
		}
	}()
	return ch
}

func (e *InteractiveExecutor) start(ctx context.Context, params map[string]interface{}, ch chan<- OutputLine) {
	prog, _ := params["program"].(string)

	sessionMu.Lock()
	if activeSession != nil {
		cleanupSessionLocked()
	}
	sessionMu.Unlock()

	sess := &GdbSession{clientTyp: e.clientTyp}

	switch e.clientTyp {
	case ConnLocal:
		if err := startGdbLocal(sess, prog); err != nil {
			ch <- OutputLine{Type: "error", Msg: err.Error()}
			return
		}
	case ConnSSH:
		if err := startGdbSSH(sess, prog); err != nil {
			ch <- OutputLine{Type: "error", Msg: err.Error()}
			return
		}
	case ConnTelnet:
		if err := startGdbTelnet(sess, prog); err != nil {
			ch <- OutputLine{Type: "error", Msg: err.Error()}
			return
		}
	case ConnADB:
		if err := startGdbADB(sess, prog); err != nil {
			ch <- OutputLine{Type: "error", Msg: err.Error()}
			return
		}
	}

	if err := waitForPrompt(ctx, sess, ch); err != nil {
		cleanupSession(sess)
		ch <- OutputLine{Type: "error", Msg: "gdb start failed: " + err.Error()}
		return
	}

	sessionMu.Lock()
	activeSession = sess
	sessionMu.Unlock()

	ch <- OutputLine{Type: "result", Ok: true, Data: "gdb started"}
	ch <- OutputLine{Type: "exit", Code: 0}
}

func (e *InteractiveExecutor) cmd(ctx context.Context, params map[string]interface{}, ch chan<- OutputLine) {
	sessionMu.Lock()
	sess := activeSession
	sessionMu.Unlock()

	if sess == nil {
		ch <- OutputLine{Type: "error", Msg: "no active gdb session"}
		return
	}

	cmdStr, _ := params["cmd"].(string)
	if cmdStr == "" {
		ch <- OutputLine{Type: "error", Msg: "empty gdb command"}
		return
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()

	_, err := fmt.Fprintln(sess.stdin, cmdStr)
	if err != nil {
		ch <- OutputLine{Type: "error", Msg: "write to gdb failed: " + err.Error()}
		cleanupSession(sess)
		sessionMu.Lock()
		activeSession = nil
		sessionMu.Unlock()
		return
	}

	if err := waitForPrompt(ctx, sess, ch); err != nil {
		cleanupSession(sess)
		sessionMu.Lock()
		activeSession = nil
		sessionMu.Unlock()
		if err == io.EOF || err.Error() == "EOF" {
			ch <- OutputLine{Type: "exit", Code: 0}
		} else {
			ch <- OutputLine{Type: "error", Msg: "gdb command failed: " + err.Error()}
		}
		return
	}

	ch <- OutputLine{Type: "exit", Code: 0}
}

func (e *InteractiveExecutor) exit(ctx context.Context, ch chan<- OutputLine) {
	sessionMu.Lock()
	sess := activeSession
	sessionMu.Unlock()

	if sess == nil {
		ch <- OutputLine{Type: "error", Msg: "no active gdb session"}
		return
	}

	sess.mu.Lock()
	fmt.Fprintln(sess.stdin, "quit")
	sess.mu.Unlock()

	done := make(chan error, 1)
	switch sess.clientTyp {
	case ConnLocal:
		go func() { done <- sess.localCmd.Wait() }()
	case ConnSSH:
		go func() { done <- sess.sshSession.Wait() }()
	case ConnTelnet:
		go func() {
			buf := make([]byte, 4096)
			for {
				_, err := sess.stdout.Read(buf)
				if err != nil {
					done <- err
					return
				}
			}
		}()
	case ConnADB:
		go func() { done <- sess.localCmd.Wait() }()
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}

	cleanupSession(sess)
	sessionMu.Lock()
	activeSession = nil
	sessionMu.Unlock()

	ch <- OutputLine{Type: "result", Ok: true}
	ch <- OutputLine{Type: "exit", Code: 0}
}

func startGdbLocal(sess *GdbSession, prog string) error {
	args := []string{"-q"}
	if prog != "" {
		args = append(args, prog)
	}
	cmd := exec.Command("gdb", args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start gdb: %w", err)
	}

	sess.stdin = stdin
	sess.stdout = stdout
	sess.stderr = stderr
	sess.localCmd = cmd
	return nil
}

func waitForPrompt(ctx context.Context, sess *GdbSession, ch chan<- OutputLine) error {
	reader := bufio.NewReader(sess.stdout)
	promptBytes := []byte(gdbPrompt)
	lineBuf := make([]byte, 0, 512)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		b, err := reader.ReadByte()
		if err != nil {
			return err
		}

		sess.promptBuf.WriteByte(b)
		lineBuf = append(lineBuf, b)

		if b == '\n' {
			line := bytes.TrimRight(lineBuf, "\r\n")
			if len(line) > 0 {
				select {
				case ch <- OutputLine{Type: "stdout", Data: string(line)}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			lineBuf = lineBuf[:0]
		}

		if bytes.HasSuffix(sess.promptBuf.Bytes(), promptBytes) {
			if len(lineBuf) > 0 {
				trimmed := bytes.TrimRight(bytes.TrimSuffix(lineBuf, promptBytes), "\r\n")
				if len(trimmed) > 0 {
					select {
					case ch <- OutputLine{Type: "stdout", Data: string(trimmed)}:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
			sess.promptBuf.Reset()
			return nil
		}
	}
}

func cleanupSession(sess *GdbSession) {
	if sess == nil {
		return
	}
	if sess.stdin != nil {
		sess.stdin.Close()
	}
	if sess.sshSession != nil {
		sess.sshSession.Close()
	}
	if sess.localCmd != nil && sess.localCmd.Process != nil {
		sess.localCmd.Process.Kill()
	}
}

func cleanupSessionLocked() {
	if activeSession != nil {
		cleanupSession(activeSession)
		activeSession = nil
	}
}

func cleanupInteractiveSession() {
	sessionMu.Lock()
	cleanupSessionLocked()
	sessionMu.Unlock()
}
