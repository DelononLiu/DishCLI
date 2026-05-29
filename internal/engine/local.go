package engine

import (
	"bufio"
	"context"
	"os/exec"
)

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
