package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
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

type SSHClient struct {
	client  *ssh.Client
	session *ssh.Session
}

var (
	sshClient      *SSHClient
	sshMutex       sync.Mutex
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

	addr := fmt.Sprintf("%s:%d", host, port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		writeError(writer, req.ReqId, "dial failed: "+err.Error())
		return
	}

	sshMutex.Lock()
	if sshClient != nil && sshClient.session != nil {
		sshClient.session.Close()
	}
	if sshClient != nil && sshClient.client != nil {
		sshClient.client.Close()
	}
	sshClient = &SSHClient{client: client}
	sshMutex.Unlock()

	writeResult(writer, req.ReqId, true)
}

func handleExec(writer *bufio.Writer, req Request) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	sshMutex.Lock()
	if sshClient == nil || sshClient.client == nil {
		sshMutex.Unlock()
		writeError(writer, req.ReqId, "not connected")
		return
	}

	if sshClient.session != nil {
		sshClient.session.Close()
	}

	session, err := sshClient.client.NewSession()
	if err != nil {
		sshMutex.Unlock()
		writeError(writer, req.ReqId, "create session failed: "+err.Error())
		return
	}
	sshClient.session = session
	sshMutex.Unlock()

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
			sshMutex.Lock()
			if sshClient.session == session {
				sshClient.session = nil
			}
			sshMutex.Unlock()
			return
		case <-ctx.Done():
			session.Signal(ssh.SIGKILL)
			writeError(writer, req.ReqId, "command timeout")
			return
		}
	}
}

func handleClose(writer *bufio.Writer, req Request) {
	sshMutex.Lock()
	if sshClient != nil {
		if sshClient.session != nil {
			sshClient.session.Close()
			sshClient.session = nil
		}
		if sshClient.client != nil {
			sshClient.client.Close()
		}
		sshClient = nil
	}
	sshMutex.Unlock()

	writeResult(writer, req.ReqId, true)
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