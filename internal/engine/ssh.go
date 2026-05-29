package engine

import (
	"bufio"
	"context"
	"fmt"

	"golang.org/x/crypto/ssh"
)

func dialSSH(host, port, user, password, privateKey string) (*ssh.Client, error) {
	config := &ssh.ClientConfig{
		User:            user,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         defaultTimeout,
	}

	if privateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(privateKey))
		if err != nil {
			return nil, err
		}
		config.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	} else if password != "" {
		config.Auth = []ssh.AuthMethod{ssh.Password(password)}
	} else {
		return nil, nil
	}

	return ssh.Dial("tcp", host+":"+port, config)
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

func startGdbSSH(sess *GdbSession, prog string) error {
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
