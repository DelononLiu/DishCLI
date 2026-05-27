package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const gdbPrompt = "(gdb) "

type GdbSession struct {
	mu         sync.Mutex
	clientTyp  connType
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
	clientTyp connType
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
	case connLocal:
		if err := e.startLocal(sess, prog); err != nil {
			ch <- OutputLine{Type: "error", Msg: err.Error()}
			return
		}
	case connSSH:
		if err := e.startSSH(sess, prog); err != nil {
			ch <- OutputLine{Type: "error", Msg: err.Error()}
			return
		}
	case connTelnet:
		if err := e.startTelnet(sess, prog); err != nil {
			ch <- OutputLine{Type: "error", Msg: err.Error()}
			return
		}
	case connADB:
		ch <- OutputLine{Type: "error", Msg: "gdb over ADB not supported"}
		return
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
	go func() {
		switch sess.clientTyp {
		case connLocal:
			done <- sess.localCmd.Wait()
		case connSSH:
			done <- sess.sshSession.Wait()
		case connTelnet:
			done <- nil
		}
	}()

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

func (e *InteractiveExecutor) startLocal(sess *GdbSession, prog string) error {
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

func (e *InteractiveExecutor) startSSH(sess *GdbSession, prog string) error {
	mu.Lock()
	sshClient := client.sshClient
	mu.Unlock()

	if sshClient == nil {
		return fmt.Errorf("not connected to SSH")
	}

	session, err := sshClient.NewSession()
	if err != nil {
		return fmt.Errorf("new ssh session: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		session.Close()
		return fmt.Errorf("stderr pipe: %w", err)
	}

	gdbCmd := "gdb -q"
	if prog != "" {
		gdbCmd += " " + prog
	}
	if err := session.Start(gdbCmd); err != nil {
		session.Close()
		return fmt.Errorf("start gdb on remote: %w", err)
	}

	sess.stdin = stdin
	sess.stdout = stdout
	sess.stderr = stderr
	sess.sshSession = session
	return nil
}

func (e *InteractiveExecutor) startTelnet(sess *GdbSession, prog string) error {
	mu.Lock()
	conn := client.telnetConn
	mu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected via telnet")
	}

	gdbCmd := "gdb -q"
	if prog != "" {
		gdbCmd += " " + prog
	}

	_, err := fmt.Fprintln(conn, gdbCmd)
	if err != nil {
		return fmt.Errorf("write gdb command to telnet: %w", err)
	}

	pr, pw := io.Pipe()

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			clean := handleTelnetNegotiation(conn, buf[:n])
			if len(clean) > 0 {
				pw.Write(clean)
			}
		}
	}()

	sess.stdin = conn
	sess.stdout = pr
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

// ─── Shell Session (general interactive shell with push-based stdout) ───

var (
	activeShell *ShellSession
	shellSessMu sync.Mutex
)

type ShellSession struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	cancel  context.CancelFunc
	mu      sync.Mutex
	buf     bytes.Buffer
	started bool
}

func newShellSession(shell string, cwd string) *ShellSession {
	ctx, cancel := context.WithCancel(context.Background())

	args := []string{"--norc", "--noprofile"}
	if cwd != "" {
		args = append(args, "--cd", cwd)
	}
	args = append(args, "-i")

	cmd := exec.CommandContext(ctx, shell, args...)

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

	// Background reader for stdout — pushes events to os.Stdout directly
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
				sess.mu.Lock()
				sess.buf.Write(buf[:n])
				sess.mu.Unlock()
				// Push to KCode immediately
				writeShellOutput(chunk)
			}
			if err != nil {
				return
			}
		}
	}()

	// Background reader for stderr (merged into same buffer and push)
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
				sess.mu.Lock()
				sess.buf.Write(buf[:n])
				sess.mu.Unlock()
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
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data += "\n"
	}
	_, err := fmt.Fprint(s.stdin, data)
	return err
}

func (s *ShellSession) readOutput() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buf.Len() == 0 {
		return ""
	}
	data := s.buf.String()
	s.buf.Reset()
	return data
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

// ─── Shell output push helpers (called from background goroutines) ───

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
