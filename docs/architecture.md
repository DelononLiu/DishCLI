# 架构设计与技术选型

## 产品定位

**dish** 是跨平台的命令执行网关，支持多协议（SSH / Telnet / ADB / Local）、多路复用、MCP 模式（stdio），输出结构化 JSON，同时提供终端字节流和纯文本双视图。

| 命令 | 模式 | 消费者 | 输出 |
|------|------|--------|------|
| `dish` | 交互式终端 | **人** | raw 终端流 |
| `dish acp` | AI 协议 | **AI Agent / 程序** | JSON over stdio |
| `dish serve` | WS 守护进程 | **多客户端** | WebSocket JSON |

## 后端引擎

```
JSON stdio / WS
  ├── local  (creack/pty)
  ├── ssh    (golang.org/x/crypto/ssh)
  ├── telnet (net.Dial + negotiation)
  └── adb    (adb shell CLI)
```

四种后端统一为同一个引擎接口，支持 PTY 分配开关。

## PTY 与输出视图

PTY 分配决定输出格式：

| PTY | 输出 | 适用场景 |
|-----|------|---------|
| 无 | `data`（纯文本） | exec 单次命令、AI tool_call |
| 有 | `raw`（含 ANSI）+ `text`（纯文本） | 交互式 Shell、人类终端 |

```json
// 无 PTY
{"type":"stdout","data":"total 42\nrw-r--r--  file.txt\n"}

// 有 PTY
{"type":"stdout","raw":"total 42\n\u001b[32mfile\u001b[0m\n","text":"total 42\nfile\n"}
```

`raw` 给人类终端渲染，`text` 给 AI / TerminalLog。

## 与同类产品的关系

| 产品 | 定位 | 职责边界 |
|------|------|----------|
| **node-pty** | Node.js PTY 引擎 | 创建和管理本地伪终端，不做协议抽象 |
| **libghostty** | Zig/C 终端模拟器引擎 | VT 解析、GPU 渲染、高性能 PTY，不涉远端执行 |
| **tmux/tmate** | 终端多路复用器 | 多客户端同屏共享，输出 raw VT 序列给人眼 |
| **dish** | 命令执行网关 | 统一 Local/SSH/Telnet/ADB，多路复用，输出 JSON 给程序 |

区分关键：dish 不是 PTY 引擎（node-pty/libghostty 处理终端 IO），也不是终端多路复用器（tmux/tmate 输出 raw VT 给人眼），而是**连接执行环境和 AI Agent 的网关**——程序发 JSON，它帮你跑到目标机器上，再把结果转成 JSON 回来。

## 多路复用

### 当前状态：不支持

所有资源均为单例（`activeShell`、`activeSession`、`client`），没有客户端列表和广播机制。

### 方案：`serve` 子命令

多路复用由 `dish serve` 子命令承载，不另做二进制：

```
┌────────────────────────────────────────┐
│  dish serve :9760                    │
│                                         │
│  ┌─ WebSocket Server ────────────────┐  │
│  │  /session/{taskId}                 │  │
│  │  Client A (KCode)            ←──  │  │
│  │  Client B (Web UI)           ←──  │  │
│  └───────────────────────────────────┘  │
│                                         │
│  ┌─ Session Manager ─────────────────┐  │
│  │  sessionId → PTY                   │  │
│  │  输入仲裁 (写锁/焦点切换)           │  │
│  │  输出广播                          │  │
│  ├────────────────────────────────────┤  │
│  │  Engine (shared library)           │  │
│  │  Local (creack/pty)                │  │
│  │  SSH / Telnet / ADB                │  │
│  └────────────────────────────────────┘  │
│                                         │
│  ┌─ TerminalLog ─────────────────────┐  │
│  │  text 输出持久化                    │  │
│  └────────────────────────────────────┘  │
└──────────────────────────────────────────┘
```

## 目录结构

```
dish/
├── internal/
│   ├── engine/          ← 共享引擎，不绑定传输层
│   │   ├── pty.go       (creack/pty 封装)
│   │   ├── ssh.go       (golang.org/x/crypto/ssh)
│   │   ├── telnet.go    (net.Dial + negotiation)
│   │   ├── adb.go       (adb shell)
│   │   └── gdb.go       (GDB 状态机)
│   ├── server/          ← WS 服务端、session 管理、广播
│   └── log/             ← TerminalLog 持久化
├── cmd/
│   └── dish/
│       └── main.go      ← 入口: 默认 / acp / serve
```

三种子命令共享同一套 `internal/engine/`。

## libghostty 集成分析

| 考量 | 结论 |
|------|------|
| 语言 | Zig/C → Go CGo，破坏交叉编译 |
| 能力匹配 | VT 解析 + 渲染，dish 只需要 PTY 创建 + 字节流透传 |
| 现有替代 | `creack/pty`（纯 Go）满足本地 PTY 需求 |

**结论**：不推荐集成。本地 PTY 用 `creack/pty`。

## 术语

| 术语 | 说明 |
|------|------|
| **伪终端 (PTY)** | 终端交互的底层载体，Shell 和远程连接均基于它 |
| **终端多路复用** | 单 PTY 对接多客户端，实时同屏共享操作 |
| **会话持久化** | 多路复用基础，保证后台会话不随连接断开销毁 |
| **日志 + 快照** | 附加能力，负责会话回退和回放 |
