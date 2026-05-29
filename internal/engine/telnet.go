package engine

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"
)

func dialTelnet(addr string) (net.Conn, error) {
	return net.DialTimeout("tcp", addr, 10*time.Second)
}

func telnetExec(ctx context.Context, c *Client, cmd string, ch chan<- OutputLine) {
	conn := c.telnetConn

	_, err := conn.Write([]byte(cmd + "\r\n"))
	if err != nil {
		ch <- OutputLine{Type: "error", Msg: "write failed: " + err.Error()}
		return
	}

	output := make(chan string, 200)
	go telnetOutputReader(conn, output, ctx)

	idleTimer := time.NewTimer(500 * time.Millisecond)
	defer idleTimer.Stop()

	for {
		select {
		case line, ok := <-output:
			if !ok {
				ch <- OutputLine{Type: "exit", Code: 0}
				return
			}
			ch <- OutputLine{Type: "stdout", Data: line}
			idleTimer.Reset(500 * time.Millisecond)
		case <-idleTimer.C:
			ch <- OutputLine{Type: "exit", Code: 0}
			return
		case <-ctx.Done():
			ch <- OutputLine{Type: "error", Msg: "command timeout"}
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

func startGdbTelnet(sess *GdbSession, prog string) error {
	mu.Lock()
	conn := client.telnetConn
	mu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected via telnet")
	}

	gdbCmd := "gdb -q"
	if prog != "" {
		gdbCmd += " " + prog
	}

	_, err := fmt.Fprintln(conn, gdbCmd)
	if err != nil {
		return fmt.Errorf("write gdb command to telnet: %w", err)
	}

	pr, pw := io.Pipe()

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			clean := handleTelnetNegotiation(conn, buf[:n])
			if len(clean) > 0 {
				pw.Write(clean)
			}
		}
	}()

	sess.stdin = conn
	sess.stdout = pr
	return nil
}
