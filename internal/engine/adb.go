package engine

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

func dialADB(host, addr string) (string, error) {
	if host != "" {
		out, err := exec.Command("adb", "connect", addr).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("%s", string(out))
		}
		return addr, nil
	}

	out, err := exec.Command("adb", "devices").Output()
	if err != nil {
		return "", err
	}
	if !hasDevice(out) {
		return "", fmt.Errorf("no adb device found")
	}
	return "", nil
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

func hasDevice(out []byte) bool {
	lines := bytes.Split(out, []byte("\n"))
	for _, line := range lines {
		trim := bytes.TrimSpace(line)
		if len(trim) == 0 {
			continue
		}
		if bytes.Contains(trim, []byte("\tdevice")) {
			return true
		}
	}
	return false
}

func startGdbADB(sess *GdbSession, prog string) error {
	mu.Lock()
	addr := client.adbAddr
	mu.Unlock()

	args := []string{"shell", "gdb -q"}
	if prog != "" {
		args[1] += " " + prog
	}
	if addr != "" {
		args = append([]string{"-s", addr}, args...)
	}

	cmd := exec.Command("adb", args...)

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
		return fmt.Errorf("start gdb over adb: %w", err)
	}

	sess.stdin = stdin
	sess.stdout = stdout
	sess.stderr = stderr
	sess.localCmd = cmd
	return nil
}
