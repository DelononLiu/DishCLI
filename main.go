package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
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
	Ok    bool   `json:"ok,omitempty"`
	Data  string `json:"data,omitempty"`
	Code  int    `json:"code,omitempty"`
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
		case "close":
			handleClose(writer, req)
		default:
			handleExecAction(writer, req)
		}
	}
}

func handleExecAction(writer *bufio.Writer, req Request) {
	mu.Lock()
	if client == nil {
		mu.Unlock()
		writeError(writer, req.ReqId, "not connected")
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
		writeError(writer, req.ReqId, "unknown action")
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
		writeJSON(writer, resp)
	}
}

func printHelp() {
	fmt.Println(`Usage: dishcli [options]

Options:
  --json    Enable JSON RPC stdio mode (required for normal operation)
  --help, -h  Show this help message

In JSON RPC stdio mode, dishcli reads JSON requests from stdin and writes JSON responses to stdout.
Each request should be a JSON object with the following fields:
  - reqId: string (request ID)
  - action: string (one of: "connect", "exec", "gdbStart", "gdbCmd", "gdbExit", "close")
  - params: object (parameters specific to the action)

Examples:
  echo '{"reqId":"1","action":"connect","params":{"type":"local"}}' | dishcli --json
  echo '{"reqId":"2","action":"exec","params":{"cmd":"echo hello"}}' | dishcli --json`)
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
