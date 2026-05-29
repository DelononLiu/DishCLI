package engine

import (
	"context"
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
