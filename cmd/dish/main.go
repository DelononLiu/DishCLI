package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"dish/internal/engine"
)

func main() {
	if len(os.Args) >= 2 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		printHelp()
		return
	}

	if len(os.Args) >= 2 && (os.Args[1] == "acp" || os.Args[1] == "--json") {
		runACP()
		return
	}

	printHelp()
}

func runACP() {
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM)

	reqCh := make(chan engine.Request, 100)
	go func() {
		defer close(reqCh)
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			var req engine.Request
			if err := json.Unmarshal([]byte(line), &req); err != nil {
				resp := engine.Response{ReqId: req.ReqId, Type: "error", Msg: "invalid json"}
				writeJSON(resp)
				continue
			}
			reqCh <- req
		}
	}()

	for req := range reqCh {
		switch req.Action {
		case "connect":
			engine.HandleConnect(req)
		case "close":
			engine.HandleClose(req)
		case "shellStart":
			engine.HandleShellStart(req)
		case "shellWrite":
			engine.HandleShellWrite(req)
		case "shellStop":
			engine.HandleShellStop(req)
		default:
			engine.HandleExecAction(req)
		}
	}
}

func writeJSON(v interface{}) {
	data, _ := json.Marshal(v)
	os.Stdout.Write(data)
	os.Stdout.Write([]byte{'\n'})
}

func printHelp() {
	fmt.Println(`Usage: dish <command> [options]

Commands:
  acp         AI protocol mode (JSON-RPC over stdio, for AI Agents)

Options:
  --json      Backward-compatible alias for 'acp'
  --help, -h  Show this help message

Examples:
  echo '{"reqId":"1","action":"connect","params":{"type":"local"}}' | dish acp
  echo '{"reqId":"2","action":"exec","params":{"cmd":"echo hello"}}' | dish acp`)
}
