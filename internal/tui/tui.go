package tui

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func Run() error {
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("raw terminal: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	cmd := exec.Command("bash", "--norc", "--noprofile", "-i")
	master, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("start shell: %w", err)
	}
	defer master.Close()

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				os.Stdout.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				master.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	<-done
	return nil
}
