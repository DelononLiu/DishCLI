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

var (
	defaultTimeout = 30 * time.Second
	stdoutMu      sync.Mutex
)

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

	reqCh := make(chan Request, 100)
	go func() {
		defer close(reqCh)
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			var req Request
			if err := json.Unmarshal([]byte(line), &req); err != nil {
				resp := Response{ReqId: req.ReqId, Type: "error", Msg: "invalid json"}
				writeResp(resp)
				continue
			}
			reqCh <- req
		}
	}()

	for req := range reqCh {
		switch req.Action {
		case "connect":
			handleConnect(req)
		case "close":
			handleClose(req)
		case "shellStart":
			handleShellStart(req)
		case "shellWrite":
			handleShellWrite(req)
		case "shellStop":
			handleShellStop(req)
		default:
			handleExecAction(req)
		}
	}
}

func handleExecAction(req Request) {
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

func handleShellStart(req Request) {
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

func handleShellWrite(req Request) {
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

func handleShellStop(req Request) {
	shellSessMu.Lock()
	killShellLocked()
	shellSessMu.Unlock()

	writeResp(Response{ReqId: req.ReqId, Type: "result", Ok: true})
	writeResp(Response{ReqId: req.ReqId, Type: "exit", Code: 0})
}

func printHelp() {
	fmt.Println(`Usage: dishcli [options]

Options:
  --json    Enable JSON RPC stdio mode (required for normal operation)
  --help, -h  Show this help message

In JSON RPC stdio mode, dishcli reads JSON requests from stdin and writes JSON responses to stdout.
Each request should be a JSON object with the following fields:
  - reqId: string (request ID)
  - action: string (one of: "connect", "exec", "gdbStart", "gdbCmd", "gdbExit",
            "shellStart", "shellWrite", "shellRead", "shellStop", "close")
  - params: object (parameters specific to the action)

Actions:
  connect       Connect to a device (SSH/telnet/ADB/local)
  exec          Run a one-shot command
  gdbStart      Start interactive GDB session
  gdbCmd        Send GDB command
  gdbExit       Exit GDB session
  shellStart    Start interactive shell (params: shell, cwd)
  shellWrite    Write data to shell stdin (params: data)
  shellStop     Stop interactive shell
  close         Disconnect device

Examples:
  echo '{"reqId":"1","action":"connect","params":{"type":"local"}}' | dishcli --json
  echo '{"reqId":"2","action":"exec","params":{"cmd":"echo hello"}}' | dishcli --json
  echo '{"reqId":"3","action":"shellStart","params":{"shell":"bash","cwd":"/tmp"}}' | dishcli --json`)
}

// ─── Output helpers (mutex-protected, write directly to os.Stdout) ───

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
