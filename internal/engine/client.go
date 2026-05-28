package engine

import (
	"bytes"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type ConnType int

const (
	ConnSSH ConnType = iota
	ConnTelnet
	ConnADB
	ConnLocal
)

type Client struct {
	typ ConnType

	sshClient  *ssh.Client
	sshSession *ssh.Session

	telnetConn net.Conn

	adbAddr string
}

var (
	client *Client
	mu     sync.Mutex
)

func HandleConnect(req Request) {
	params := req.Params
	host, _ := params["host"].(string)
	port := 22
	if p, ok := params["port"].(float64); ok {
		port = int(p)
	}

	typ := ConnSSH
	switch t, _ := params["type"].(string); t {
	case "telnet":
		typ = ConnTelnet
	case "adb":
		typ = ConnADB
	case "local":
		typ = ConnLocal
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))

	if typ == ConnLocal {
		mu.Lock()
		closeClientLocked()
		client = &Client{typ: ConnLocal}
		mu.Unlock()

		writeResult(req.ReqId, true)
		return
	}

	if typ == ConnTelnet {
		conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
		if err != nil {
			writeError(req.ReqId, "dial failed: "+err.Error())
			return
		}

		mu.Lock()
		closeClientLocked()
		client = &Client{typ: ConnTelnet, telnetConn: conn}
		mu.Unlock()

		writeResult(req.ReqId, true)
		return
	}

	if typ == ConnADB {
		if host != "" {
			out, err := exec.Command("adb", "connect", addr).CombinedOutput()
			if err != nil {
				writeError(req.ReqId, "adb connect failed: "+string(out))
				return
			}
		} else {
			out, err := exec.Command("adb", "devices").Output()
			if err != nil {
				writeError(req.ReqId, "adb devices failed: "+err.Error())
				return
			}
			if !hasDevice(out) {
				writeError(req.ReqId, "no adb device found")
				return
			}
		}

		adbAddr := addr
		if host == "" {
			adbAddr = ""
		}

		mu.Lock()
		closeClientLocked()
		client = &Client{typ: ConnADB, adbAddr: adbAddr}
		mu.Unlock()

		writeResult(req.ReqId, true)
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
			writeError(req.ReqId, "parse privateKey failed: "+err.Error())
			return
		}
		config.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	} else if password != "" {
		config.Auth = []ssh.AuthMethod{ssh.Password(password)}
	} else {
		writeError(req.ReqId, "no auth method provided")
		return
	}

	c, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		writeError(req.ReqId, "dial failed: "+err.Error())
		return
	}

	mu.Lock()
	closeClientLocked()
	client = &Client{typ: ConnSSH, sshClient: c}
	mu.Unlock()

	writeResult(req.ReqId, true)
}

func HandleClose(req Request) {
	mu.Lock()
	closeClientLocked()
	mu.Unlock()

	writeResult(req.ReqId, true)
}

func closeClientLocked() {
	killShellLocked()
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
	if client.typ == ConnADB && client.adbAddr != "" {
		exec.Command("adb", "disconnect", client.adbAddr).Run()
	}
	cleanupInteractiveSession()
	client = nil
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
