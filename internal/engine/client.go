package engine

import (
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

	typ := ConnSSH
	switch t, _ := params["type"].(string); t {
	case "telnet":
		typ = ConnTelnet
	case "adb":
		typ = ConnADB
	case "local":
		typ = ConnLocal
	}

	port := 22
	if p, ok := params["port"].(float64); ok {
		port = int(p)
	} else if typ == ConnTelnet {
		port = 23
	} else if typ == ConnADB {
		port = 5555
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))

	mu.Lock()
	closeClientLocked()
	c := &Client{typ: typ}
	client = c
	mu.Unlock()

	switch typ {
	case ConnLocal:
		writeResult(req.ReqId, true)
		return

	case ConnTelnet:
		conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
		if err != nil {
			writeError(req.ReqId, "dial failed: "+err.Error())
			return
		}
		c.telnetConn = conn
		writeResult(req.ReqId, true)
		return

	case ConnADB:
		out, err := exec.Command("adb", "connect", addr).CombinedOutput()
		if err != nil {
			writeError(req.ReqId, "adb connect failed: "+string(out))
			client = nil
			return
		}
		c.adbAddr = addr
		writeResult(req.ReqId, true)
		return
	}

	user, _ := params["user"].(string)
	password, _ := params["password"].(string)
	privateKey, _ := params["privateKey"].(string)

	sshClient, err := dialSSH(host, strconv.Itoa(port), user, password, privateKey)
	if err != nil {
		writeError(req.ReqId, "ssh dial failed: "+err.Error())
		client = nil
		return
	}
	if sshClient == nil {
		writeError(req.ReqId, "no auth method provided")
		client = nil
		return
	}
	c.sshClient = sshClient
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
