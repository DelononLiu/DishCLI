package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"dish/internal/engine"
	"dish/internal/tui"
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

	if err := tui.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
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
	fmt.Println(`Usage: dish [command] [options]

Commands:
  (default)   Interactive TUI mode (interactive terminal)
  acp         AI protocol mode (JSON-RPC over stdio, for AI Agents)

Options:
  --json      Backward-compatible alias for 'acp'
  --help, -h  Show this help message

Examples:
  dish                              Start interactive TUI
  echo '{"reqId":"1","action":"exec"}' | dish acp`)
}
