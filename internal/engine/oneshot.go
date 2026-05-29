package engine

import (
	"bufio"
	"context"
	"net"
	"os/exec"
	"time"

	"golang.org/x/crypto/ssh"
)

type OneShotExecutor struct{}

func (e *OneShotExecutor) Exec(ctx context.Context, action string, params map[string]interface{}) <-chan OutputLine {
	ch := make(chan OutputLine)
	go func() {
		defer close(ch)

		cmdStr, _ := params["cmd"].(string)
		if cmdStr == "" {
			ch <- OutputLine{Type: "error", Msg: "empty command"}
			return
		}

		usePTY, _ := params["pty"].(bool)

		mu.Lock()
		if client == nil {
			mu.Unlock()
			ch <- OutputLine{Type: "error", Msg: "not connected"}
			return
		}
		c := client
		mu.Unlock()

		switch c.typ {
		case ConnSSH:
			if usePTY {
				sshExecPTY(ctx, c, cmdStr, ch)
			} else {
				sshExec(ctx, c, cmdStr, ch)
			}
		case ConnTelnet:
			telnetExec(ctx, c, cmdStr, ch)
		case ConnADB:
			adbExec(ctx, c, cmdStr, ch)
		case ConnLocal:
			if usePTY {
				localExecPTY(ctx, cmdStr, ch)
			} else {
				localExec(ctx, cmdStr, ch)
			}
		}
	}()
	return ch
}

func sshExec(ctx context.Context, c *Client, cmd string, ch chan<- OutputLine) {
	mu.Lock()
	sshClient := c.sshClient
	mu.Unlock()

	if sshClient == nil {
		ch <- OutputLine{Type: "error", Msg: "ssh client is nil"}
		return
	}

	session, err := sshClient.NewSession()
	if err != nil {
		ch <- OutputLine{Type: "error", Msg: "create session failed: " + err.Error()}
		return
	}
	defer session.Close()

	stdout, err := session.StdoutPipe()
	if err != nil {
		ch <- OutputLine{Type: "error", Msg: "stdout pipe failed: " + err.Error()}
		return
	}

	stderr, err := session.StderrPipe()
	if err != nil {
		ch <- OutputLine{Type: "error", Msg: "stderr pipe failed: " + err.Error()}
		return
	}

	if err := session.Start(cmd); err != nil {
		ch <- OutputLine{Type: "error", Msg: "exec failed: " + err.Error()}
		return
	}

	stdoutCh := make(chan string, 100)
	stderrCh := make(chan string, 100)
	doneCh := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			select {
			case stdoutCh <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			select {
			case stderrCh <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		doneCh <- session.Wait()
	}()

	for {
		select {
		case line := <-stdoutCh:
			ch <- OutputLine{Type: "stdout", Data: line}
		case line := <-stderrCh:
			ch <- OutputLine{Type: "stderr", Data: line}
		case err := <-doneCh:
			exitCode := 0
			if err != nil {
				if exitErr, ok := err.(*ssh.ExitError); ok {
					exitCode = exitErr.ExitStatus()
				} else {
					exitCode = 255
				}
			}
			ch <- OutputLine{Type: "exit", Code: exitCode}
			return
		case <-ctx.Done():
			session.Signal(ssh.SIGKILL)
			ch <- OutputLine{Type: "error", Msg: "command timeout"}
			return
		}
	}
}

func sshExecPTY(ctx context.Context, c *Client, cmd string, ch chan<- OutputLine) {
	mu.Lock()
	sshClient := c.sshClient
	mu.Unlock()

	if sshClient == nil {
		ch <- OutputLine{Type: "error", Msg: "ssh client is nil"}
		return
	}

	session, err := sshClient.NewSession()
	if err != nil {
		ch <- OutputLine{Type: "error", Msg: "create session failed: " + err.Error()}
		return
	}
	defer session.Close()

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm", 80, 24, modes); err != nil {
		ch <- OutputLine{Type: "error", Msg: "request pty failed: " + err.Error()}
		return
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		ch <- OutputLine{Type: "error", Msg: "stdout pipe failed: " + err.Error()}
		return
	}

	if err := session.Start(cmd); err != nil {
		ch <- OutputLine{Type: "error", Msg: "exec failed: " + err.Error()}
		return
	}

	outputCh := make(chan string, 100)
	doneCh := make(chan error, 1)

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				outputCh <- string(buf[:n])
			}
			if err != nil {
				close(outputCh)
				return
			}
		}
	}()

	go func() {
		doneCh <- session.Wait()
	}()

	for {
		select {
		case raw, ok := <-outputCh:
			if !ok {
				err := <-doneCh
				exitCode := 0
				if err != nil {
					if exitErr, ok := err.(*ssh.ExitError); ok {
						exitCode = exitErr.ExitStatus()
					} else {
						exitCode = 255
					}
				}
				ch <- OutputLine{Type: "exit", Code: exitCode}
				return
			}
			text := stripANSI(raw)
			ch <- OutputLine{Type: "stdout", Raw: raw, Text: text}
		case <-ctx.Done():
			session.Signal(ssh.SIGKILL)
			ch <- OutputLine{Type: "error", Msg: "command timeout"}
			return
		}
	}
}

func localExec(ctx context.Context, cmd string, ch chan<- OutputLine) {
	c := exec.Command("sh", "-c", cmd)

	stdout, err := c.StdoutPipe()
	if err != nil {
		ch <- OutputLine{Type: "error", Msg: "stdout pipe failed: " + err.Error()}
		return
	}

	stderr, err := c.StderrPipe()
	if err != nil {
		ch <- OutputLine{Type: "error", Msg: "stderr pipe failed: " + err.Error()}
		return
	}

	if err := c.Start(); err != nil {
		ch <- OutputLine{Type: "error", Msg: "exec failed: " + err.Error()}
		return
	}

	stdoutCh := make(chan string, 100)
	stderrCh := make(chan string, 100)
	doneCh := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			select {
			case stdoutCh <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			select {
			case stderrCh <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		doneCh <- c.Wait()
	}()

	for {
		select {
		case line := <-stdoutCh:
			ch <- OutputLine{Type: "stdout", Data: line}
		case line := <-stderrCh:
			ch <- OutputLine{Type: "stderr", Data: line}
		case err := <-doneCh:
			exitCode := 0
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				} else {
					exitCode = 255
				}
			}
			ch <- OutputLine{Type: "exit", Code: exitCode}
			return
		case <-ctx.Done():
			c.Process.Kill()
			ch <- OutputLine{Type: "error", Msg: "command timeout"}
			return
		}
	}
}

func localExecPTY(ctx context.Context, cmd string, ch chan<- OutputLine) {
	c := exec.Command("sh", "-c", cmd)

	master, err := startWithPTY(c)
	if err != nil {
		ch <- OutputLine{Type: "error", Msg: "pty start failed: " + err.Error()}
		return
	}
	defer master.Close()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- c.Wait()
	}()

	outputCh := make(chan string, 100)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				outputCh <- string(buf[:n])
			}
			if err != nil {
				close(outputCh)
				return
			}
		}
	}()

	for {
		select {
		case raw, ok := <-outputCh:
			if !ok {
				err := <-doneCh
				exitCode := 0
				if err != nil {
					if exitErr, ok := err.(*exec.ExitError); ok {
						exitCode = exitErr.ExitCode()
					} else {
						exitCode = 255
					}
				}
				ch <- OutputLine{Type: "exit", Code: exitCode}
				return
			}
			text := stripANSI(raw)
			ch <- OutputLine{Type: "stdout", Raw: raw, Text: text}
		case <-ctx.Done():
			c.Process.Kill()
			ch <- OutputLine{Type: "error", Msg: "command timeout"}
			return
		}
	}
}

func adbExec(ctx context.Context, c *Client, cmd string, ch chan<- OutputLine) {
	args := []string{"shell", cmd}
	if c.adbAddr != "" {
		args = append([]string{"-s", c.adbAddr}, args...)
	}

	exe := exec.Command("adb", args...)

	stdout, err := exe.StdoutPipe()
	if err != nil {
		ch <- OutputLine{Type: "error", Msg: "stdout pipe failed: " + err.Error()}
		return
	}

	stderr, err := exe.StderrPipe()
	if err != nil {
		ch <- OutputLine{Type: "error", Msg: "stderr pipe failed: " + err.Error()}
		return
	}

	if err := exe.Start(); err != nil {
		ch <- OutputLine{Type: "error", Msg: "adb exec failed: " + err.Error()}
		return
	}

	stdoutCh := make(chan string, 100)
	stderrCh := make(chan string, 100)
	doneCh := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			select {
			case stdoutCh <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			select {
			case stderrCh <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		doneCh <- exe.Wait()
	}()

	for {
		select {
		case line := <-stdoutCh:
			ch <- OutputLine{Type: "stdout", Data: line}
		case line := <-stderrCh:
			ch <- OutputLine{Type: "stderr", Data: line}
		case err := <-doneCh:
			exitCode := 0
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				} else {
					exitCode = 255
				}
			}
			ch <- OutputLine{Type: "exit", Code: exitCode}
			return
		case <-ctx.Done():
			exe.Process.Kill()
			ch <- OutputLine{Type: "error", Msg: "command timeout"}
			return
		}
	}
}

func telnetExec(ctx context.Context, c *Client, cmd string, ch chan<- OutputLine) {
	conn := c.telnetConn

	_, err := conn.Write([]byte(cmd + "\r\n"))
	if err != nil {
		ch <- OutputLine{Type: "error", Msg: "write failed: " + err.Error()}
		return
	}

	output := make(chan string, 200)
	go telnetOutputReader(conn, output, ctx)

	idleTimer := time.NewTimer(500 * time.Millisecond)
	defer idleTimer.Stop()

	for {
		select {
		case line, ok := <-output:
			if !ok {
				ch <- OutputLine{Type: "exit", Code: 0}
				return
			}
			ch <- OutputLine{Type: "stdout", Data: line}
			idleTimer.Reset(500 * time.Millisecond)
		case <-idleTimer.C:
			ch <- OutputLine{Type: "exit", Code: 0}
			return
		case <-ctx.Done():
			ch <- OutputLine{Type: "error", Msg: "command timeout"}
			return
		}
	}
}

func telnetOutputReader(conn net.Conn, ch chan<- string, ctx context.Context) {
	defer close(ch)
	buf := make([]byte, 4096)
	lineBuf := make([]byte, 0, 512)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := conn.Read(buf)
		if err != nil {
			return
		}

		data := handleTelnetNegotiation(conn, buf[:n])

		for _, b := range data {
			if b == '\n' {
				if len(lineBuf) > 0 && lineBuf[len(lineBuf)-1] == '\r' {
					lineBuf = lineBuf[:len(lineBuf)-1]
				}
				if len(lineBuf) > 0 {
					select {
					case ch <- string(lineBuf):
					case <-ctx.Done():
						return
					}
					lineBuf = lineBuf[:0]
				}
			} else if b != '\r' {
				lineBuf = append(lineBuf, b)
			}
		}
	}
}

func handleTelnetNegotiation(conn net.Conn, data []byte) []byte {
	result := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		if data[i] != 255 {
			result = append(result, data[i])
			i++
			continue
		}
		if i+1 >= len(data) {
			break
		}
		cmd := data[i+1]
		switch {
		case cmd == 255:
			result = append(result, 255)
			i += 2
		case cmd == 240:
			i += 2
		case cmd == 250:
			j := i + 2
			for j < len(data) {
				if data[j] == 255 && j+1 < len(data) && data[j+1] == 240 {
					i = j + 2
					goto next
				}
				j++
			}
			i = len(data)
		case cmd == 251:
			if i+2 < len(data) {
				conn.Write([]byte{255, 254, data[i+2]})
				i += 3
			} else {
				i = len(data)
			}
		case cmd == 253:
			if i+2 < len(data) {
				conn.Write([]byte{255, 252, data[i+2]})
				i += 3
			} else {
				i = len(data)
			}
		default:
			if cmd >= 252 && cmd <= 254 && i+2 < len(data) {
				i += 3
			} else {
				i += 2
			}
		}
	next:
	}
	return result
}
