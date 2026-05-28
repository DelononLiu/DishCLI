# dish — 跨平台的命令执行网关

支持多协议：SSH / Telnet / ADB / Local，提供多路复用、MCP 模式（stdio）、终端字节流和纯文本双视图。

## 模式

```
dish                  交互式终端（带 PTY，全屏 TUI，给人看的终端）
dish acp              AI 协议模式（JSON over stdio，给 AI Agent 调用）
dish serve [地址]      WS 守护进程（多路复用 + TerminalLog）
```

三种模式共享同一后端引擎：Local (creack/pty)、SSH、Telnet、ADB。

## 子命令

### dish — 交互式终端

默认模式，启动交互式终端会话，支持四种后端连接。

```bash
dish                                          # local 模式
dish --type ssh --host example.com --user root # SSH 模式
dish --type telnet --host 192.168.1.1          # Telnet 模式
dish --type adb                                # ADB 模式
```

### dish acp — AI 协议模式

JSON over stdio 协议，面向 AI Agent / 程序调用。当前 `--json` 行为的进化版本。

```bash
echo '{"reqId":"1","action":"connect","params":{"type":"local"}}' | dish acp
echo '{"reqId":"2","action":"exec","params":{"cmd":"ls -la"}}' | dish acp
```

### dish serve — 守护进程模式

WebSocket 多路复用守护进程，支持多客户端共享同一会话。

```bash
dish serve :9760
```

## 协议（acp 模式）

### 请求

```json
{"reqId":"<请求ID>","action":"<动作>","params":{...}}
```

### 响应

每条响应独立一行 JSON。

| type | 含义 |
|------|------|
| `stdout` | 标准输出（含 `raw`+`text` 双字段，PTY 模式下） |
| `stderr` | 错误输出 |
| `result` | 操作结果 |
| `exit` | 命令执行结束 |
| `error` | 错误信息 |

### 动作

| action | 用途 |
|--------|------|
| `connect` | 建立连接（SSH/Telnet/ADB/Local），支持 `pty: true/false` |
| `exec` | 单次执行命令，支持 `pty: true/false` |
| `gdbStart` | 启动 GDB 交互式会话 |
| `gdbCmd` | 下发 GDB 命令 |
| `gdbExit` | 退出 GDB 会话 |
| `shellStart` | 启动交互式 Shell 会话，支持 `pty: true/false` |
| `shellWrite` | 向 Shell 写入数据 |
| `shellStop` | 停止 Shell 会话 |
| `close` | 关闭连接 |

### PTY 参数

分配 PTY 时输出包含 `raw` + `text` 两个字段：

```json
// 无 PTY（纯文本）
{"reqId":"1","type":"stdout","data":"total 42\nrw-r--r--  file.txt\n"}

// 有 PTY（双视图）
{"reqId":"1","type":"stdout","raw":"total 42\n\u001b[32mfile\u001b[0m\n","text":"total 42\nfile\n"}
```

### 连接类型

| type | 底层 |
|------|------|
| `local` | `creack/pty` |
| （默认/未指定） | `golang.org/x/crypto/ssh` |
| `telnet` | `net.Dial` + Telnet negotiation |
| `adb` | `adb shell` |

## 架构

```
dish/
├── internal/
│   ├── engine/          ← 共享引擎（PTY、SSH、Telnet、ADB、GDB）
│   ├── server/          ← WS 服务端、session 管理、广播
│   └── log/             ← TerminalLog 持久化
├── cmd/
│   └── dish/
│       └── main.go      ← 入口: 默认 / acp / serve
```

## 构建

```bash
go build -o dish .
```
