package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type connType int

const (
	connSSH connType = iota
	connTelnet
	connADB
	connLocal
)

type Client struct {
	typ connType

	sshClient  *ssh.Client
	sshSession *ssh.Session

	telnetConn net.Conn

	adbAddr string
}

var (
	client *Client
	mu     sync.Mutex
)

func handleConnect(writer *bufio.Writer, req Request) {
	params := req.Params
	host, _ := params["host"].(string)
	port := 22
	if p, ok := params["port"].(float64); ok {
		port = int(p)
	}

	typ := connSSH
	switch t, _ := params["type"].(string); t {
	case "telnet":
		typ = connTelnet
	case "adb":
		typ = connADB
	case "local":
		typ = connLocal
	}

	addr := fmt.Sprintf("%s:%d", host, port)

	if typ == connLocal {
		mu.Lock()
		closeClientLocked()
		client = &Client{typ: connLocal}
		mu.Unlock()

		writeResult(writer, req.ReqId, true)
		return
	}

	if typ == connTelnet {
		conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
		if err != nil {
			writeError(writer, req.ReqId, "dial failed: "+err.Error())
			return
		}

		mu.Lock()
		closeClientLocked()
		client = &Client{typ: connTelnet, telnetConn: conn}
		mu.Unlock()

		writeResult(writer, req.ReqId, true)
		return
	}

	if typ == connADB {
		if host != "" {
			out, err := exec.Command("adb", "connect", addr).CombinedOutput()
			if err != nil {
				writeError(writer, req.ReqId, "adb connect failed: "+string(out))
				return
			}
		} else {
			out, err := exec.Command("adb", "devices").Output()
			if err != nil {
				writeError(writer, req.ReqId, "adb devices failed: "+err.Error())
				return
			}
			if !hasDevice(out) {
				writeError(writer, req.ReqId, "no adb device found")
				return
			}
		}

		adbAddr := addr
		if host == "" {
			adbAddr = ""
		}

		mu.Lock()
		closeClientLocked()
		client = &Client{typ: connADB, adbAddr: adbAddr}
		mu.Unlock()

		writeResult(writer, req.ReqId, true)
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
			writeError(writer, req.ReqId, "parse privateKey failed: "+err.Error())
			return
		}
		config.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	} else if password != "" {
		config.Auth = []ssh.AuthMethod{ssh.Password(password)}
	} else {
		writeError(writer, req.ReqId, "no auth method provided")
		return
	}

	c, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		writeError(writer, req.ReqId, "dial failed: "+err.Error())
		return
	}

	mu.Lock()
	closeClientLocked()
	client = &Client{typ: connSSH, sshClient: c}
	mu.Unlock()

	writeResult(writer, req.ReqId, true)
}

func handleClose(writer *bufio.Writer, req Request) {
	mu.Lock()
	closeClientLocked()
	mu.Unlock()

	writeResult(writer, req.ReqId, true)
}

func closeClientLocked() {
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
	if client.typ == connADB && client.adbAddr != "" {
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
