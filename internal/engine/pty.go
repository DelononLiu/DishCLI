package engine

import (
	"os"
	"os/exec"
	"regexp"
	"sync"

	"github.com/creack/pty"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\][^\x1b]*(?:\x1b\\|\x07)|\x1b[\[\]()][0-9;]*[~0-9A-Za-z]|\x1b[N-Z\\^_]|[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)

func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

var ptyMu sync.Mutex

func startWithPTY(cmd *exec.Cmd) (*os.File, error) {
	ptyMu.Lock()
	defer ptyMu.Unlock()
	return pty.Start(cmd)
}
