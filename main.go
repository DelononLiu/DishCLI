package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

type Request struct {
	ReqId  string                 `json:"reqId"`
	Action string                 `json:"action"`
	Params map[string]interface{} `json:"params"`
}

type Response struct {
	ReqId string `json:"reqId"`
	Type  string `json:"type"`
	Ok    bool   `json:"ok,omitempty"`
	Data  string `json:"data,omitempty"`
	Code  int    `json:"code,omitempty"`
	Msg   string `json:"msg,omitempty"`
}

type connType int

const (
	connSSH connType = iota
	connTelnet
	connADB
)

type Client struct {
	typ connType

	sshClient  *ssh.Client
	sshSession *ssh.Session

	telnetConn net.Conn

	adbAddr string // "host:port" for network ADB, "" for local USB
}

var (
	client         *Client
	mu             sync.Mutex
	defaultTimeout = 30 * time.Second
)

func printHelp() {
	fmt.Println(`Usage: dishcli [options]

Options:
  --json    Enable JSON RPC stdio mode (required for normal operation)
  --help, -h  Show this help message

In JSON RPC stdio mode, dishcli reads JSON requests from stdin and writes JSON responses to stdout.
Each request should be a JSON object with the following fields:
  - reqId: string (request ID)
  - action: string (one of: "connect", "exec", "close")
  - params: object (parameters specific to the action)

Examples:
  echo '{"reqId":"1","action":"connect","params":{"host":"example.com","user":"admin","password":"secret"}}' | dishcli --json
`)
}

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		printHelp()
		return
	}

	if len(os.Args) != 2 || os.Args[1] != "--json" {
		printHelp()
		return
	}

	signal.Ignore(syscall.SIGINT, syscall.SIGTERM)

	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer writer.Flush()

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			writeError(writer, req.ReqId, "invalid json")
			continue
		}

		switch req.Action {
		case "connect":
			handleConnect(writer, req)
		case "exec":
			handleExec(writer, req)
		case "close":
			handleClose(writer, req)
		default:
			writeError(writer, req.ReqId, "unknown action")
		}
	}
}

func handleConnect(writer *bufio.Writer, req Request) {
	params := req.Params
	host, _ := params["host"].(string)
	port := 22
	if p, ok := params["port"].(float64); ok {
		port = int(p)
	}

	typ := connSSH
	switch t, _ := params["type"].(string); t {
	case "telnet":
		typ = connTelnet
	case "adb":
		typ = connADB
	}

	addr := fmt.Sprintf("%s:%d", host, port)

	if typ == connTelnet {
		conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
		if err != nil {
			writeError(writer, req.ReqId, "dial failed: "+err.Error())
			return
		}

		mu.Lock()
		closeClientLocked()
		client = &Client{typ: connTelnet, telnetConn: conn}
		mu.Unlock()

		writeResult(writer, req.ReqId, true)
		return
	}

	if typ == connADB {
		if host != "" {
			out, err := exec.Command("adb", "connect", addr).CombinedOutput()
			if err != nil {
				writeError(writer, req.ReqId, "adb connect failed: "+string(out))
				return
			}
		} else {
			out, err := exec.Command("adb", "devices").Output()
			if err != nil {
				writeError(writer, req.ReqId, "adb devices failed: "+err.Error())
				return
			}
			if !hasDevice(out) {
				writeError(writer, req.ReqId, "no adb device found")
				return
			}
		}

		adbAddr := addr
		if host == "" {
			adbAddr = ""
		}

		mu.Lock()
		closeClientLocked()
		client = &Client{typ: connADB, adbAddr: adbAddr}
		mu.Unlock()

		writeResult(writer, req.ReqId, true)
		return
	}

	user, _ := params["user"].(string)
	password, _ := params["password"].(string)
	privateKey, _ := params["privateKey"].(string)

	config := &ssh.ClientConfig{
		User:            user,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	if privateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(privateKey))
		if err != nil {
			writeError(writer, req.ReqId, "parse privateKey failed: "+err.Error())
			return
		}
		config.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	} else if password != "" {
		config.Auth = []ssh.AuthMethod{ssh.Password(password)}
	} else {
		writeError(writer, req.ReqId, "no auth method provided")
		return
	}

	c, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		writeError(writer, req.ReqId, "dial failed: "+err.Error())
		return
	}

	mu.Lock()
	closeClientLocked()
	client = &Client{typ: connSSH, sshClient: c}
	mu.Unlock()

	writeResult(writer, req.ReqId, true)
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

func handleExec(writer *bufio.Writer, req Request) {
	mu.Lock()
	if client == nil {
		mu.Unlock()
		writeError(writer, req.ReqId, "not connected")
		return
	}

	if client.typ == connTelnet {
		conn := client.telnetConn
		mu.Unlock()
		handleTelnetExec(writer, req, conn)
		return
	}

	if client.typ == connADB {
		adbAddr := client.adbAddr
		mu.Unlock()
		handleADBExec(writer, req, adbAddr)
		return
	}

	if client.sshClient == nil {
		mu.Unlock()
		writeError(writer, req.ReqId, "not connected")
		return
	}

	if client.sshSession != nil {
		client.sshSession.Close()
	}

	session, err := client.sshClient.NewSession()
	if err != nil {
		mu.Unlock()
		writeError(writer, req.ReqId, "create session failed: "+err.Error())
		return
	}
	client.sshSession = session
	mu.Unlock()

	cmd, _ := req.Params["cmd"].(string)

	stdout, err := session.StdoutPipe()
	if err != nil {
		writeError(writer, req.ReqId, "stdout pipe failed: "+err.Error())
		return
	}

	stderr, err := session.StderrPipe()
	if err != nil {
		writeError(writer, req.ReqId, "stderr pipe failed: "+err.Error())
		return
	}

	if err := session.Start(cmd); err != nil {
		writeError(writer, req.ReqId, "exec failed: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

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
			writeStdout(writer, req.ReqId, line)
		case line := <-stderrCh:
			writeStderr(writer, req.ReqId, line)
		case err := <-doneCh:
			exitCode := 0
			if err != nil {
				if exitErr, ok := err.(*ssh.ExitError); ok {
					exitCode = exitErr.ExitStatus()
				} else {
					exitCode = 255
				}
			}
			writeExit(writer, req.ReqId, exitCode)
			mu.Lock()
			if client != nil && client.sshSession == session {
				client.sshSession = nil
			}
			mu.Unlock()
			return
		case <-ctx.Done():
			session.Signal(ssh.SIGKILL)
			writeError(writer, req.ReqId, "command timeout")
			return
		}
	}
}

func handleADBExec(writer *bufio.Writer, req Request, adbAddr string) {
	cmd, _ := req.Params["cmd"].(string)
	if cmd == "" {
		writeError(writer, req.ReqId, "empty command")
		return
	}

	args := []string{"shell", cmd}
	if adbAddr != "" {
		args = append([]string{"-s", adbAddr}, args...)
	}

	c := exec.Command("adb", args...)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	if err := c.Start(); err != nil {
		writeError(writer, req.ReqId, "adb exec failed: "+err.Error())
		return
	}

	done := make(chan error, 1)
	go func() {
		done <- c.Wait()
	}()

	select {
	case err := <-done:
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 255
			}
		}

		scanner := bufio.NewScanner(&stdout)
		for scanner.Scan() {
			writeStdout(writer, req.ReqId, scanner.Text())
		}

		scanner = bufio.NewScanner(&stderr)
		for scanner.Scan() {
			writeStderr(writer, req.ReqId, scanner.Text())
		}

		writeExit(writer, req.ReqId, exitCode)
	case <-ctx.Done():
		c.Process.Kill()
		writeError(writer, req.ReqId, "command timeout")
	}
}

func handleTelnetExec(writer *bufio.Writer, req Request, conn net.Conn) {
	cmd, _ := req.Params["cmd"].(string)
	if cmd == "" {
		writeError(writer, req.ReqId, "empty command")
		return
	}

	_, err := conn.Write([]byte(cmd + "\r\n"))
	if err != nil {
		writeError(writer, req.ReqId, "write failed: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	output := make(chan string, 200)
	go telnetOutputReader(conn, output, ctx)

	idleTimer := time.NewTimer(500 * time.Millisecond)
	defer idleTimer.Stop()

	for {
		select {
		case line, ok := <-output:
			if !ok {
				writeExit(writer, req.ReqId, 0)
				return
			}
			writeStdout(writer, req.ReqId, line)
			idleTimer.Reset(500 * time.Millisecond)
		case <-idleTimer.C:
			writeExit(writer, req.ReqId, 0)
			return
		case <-ctx.Done():
			writeError(writer, req.ReqId, "command timeout")
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

func handleClose(writer *bufio.Writer, req Request) {
	mu.Lock()
	closeClientLocked()
	mu.Unlock()

	writeResult(writer, req.ReqId, true)
}

func closeClientLocked() {
	if client == nil {
		return
	}
	if client.sshSession != nil {
		client.sshSession.Close()
		client.sshSession = nil
	}
	if client.sshClient != nil {
		client.sshClient.Close()
		client.sshClient = nil
	}
	if client.telnetConn != nil {
		client.telnetConn.Close()
		client.telnetConn = nil
	}
	if client.typ == connADB && client.adbAddr != "" {
		exec.Command("adb", "disconnect", client.adbAddr).Run()
	}
	client = nil
}

func writeResult(writer *bufio.Writer, reqId string, ok bool) {
	resp := Response{ReqId: reqId, Type: "result", Ok: ok}
	writeJSON(writer, resp)
}

func writeStdout(writer *bufio.Writer, reqId, data string) {
	resp := Response{ReqId: reqId, Type: "stdout", Data: data}
	writeJSON(writer, resp)
}

func writeStderr(writer *bufio.Writer, reqId, data string) {
	resp := Response{ReqId: reqId, Type: "stderr", Data: data}
	writeJSON(writer, resp)
}

func writeExit(writer *bufio.Writer, reqId string, code int) {
	resp := Response{ReqId: reqId, Type: "exit", Code: code}
	writeJSON(writer, resp)
}

func writeError(writer *bufio.Writer, reqId, msg string) {
	resp := Response{ReqId: reqId, Type: "error", Msg: msg}
	writeJSON(writer, resp)
}

func writeJSON(writer *bufio.Writer, resp Response) {
	data, _ := json.Marshal(resp)
	writer.Write(data)
	writer.WriteByte('\n')
	writer.Flush()
}
