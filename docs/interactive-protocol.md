# 协议文档（acp 模式）

## 概述

`dish acp` 是 dish 命令执行网关的 AI 协议接口，通过 STDIN/STDOUT 单行 JSON 协议提供统一流式接口。支持四种后端连接和多种交互模式。

| 模式 | 动作 | 适用场景 |
|------|------|----------|
| One-Shot | `exec` | 单条命令执行，等待完成 |
| GDB 调试 | `gdbStart` / `gdbCmd` / `gdbExit` | 交互式 C/C++ 调试 |
| Shell 会话 | `shellStart` / `shellWrite` / `shellStop` | 长时间交互式 Shell |
| 连接管理 | `connect` / `close` | 建立/关闭后端连接 |

后端连接支持：**Local**、**SSH**、**Telnet**、**ADB**。

---

## 协议格式

### 请求

```json
{"reqId":"<请求ID>","action":"<动作>","params":{...}}
```

每行一个完整 JSON 对象，通过 STDIN 写入。

### 响应

每个请求会产生 0~N 条流式响应，按顺序推送，每行一条 JSON：

```json
{"reqId":"<请求ID>","type":"<类型>","data":"...","ok":true,"code":0,"msg":"..."}
```

| type | 含义 | 存在字段 |
|------|------|----------|
| `stdout` | 标准输出 | `reqId`, `type`, `data` 或 `raw`+`text` |
| `stderr` | 错误输出 | `reqId`, `type`, `data` |
| `result` | 操作结果 | `reqId`, `type`, `ok` |
| `exit` | 命令/会话结束 | `reqId`, `type`, `code` |
| `error` | 错误信息 | `reqId`, `type`, `msg` |

### PTY 与输出格式

`exec` 和 `shellStart` 支持 `pty` 参数，决定输出格式：

**无 PTY**（默认）— 纯文本输出：
```json
{"reqId":"1","type":"stdout","data":"total 42\nrw-r--r--  file.txt\n"}
```

**有 PTY** — 双视图输出：
```json
{"reqId":"1","type":"stdout","raw":"total 42\n\u001b[32mfile\u001b[0m\n","text":"total 42\nfile\n"}
```

| 字段 | 含义 |
|------|------|
| `raw` | 原始终端字节流（含 ANSI 控制字符） |
| `text` | 剥离控制字符后的纯文本 |

---

## 1. 连接管理

### connect

建立到目标的连接，所有后续动作依赖此连接。

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `type` | string | 否 | 连接类型：`local` / `telnet` / `adb` / (默认 SSH) |
| `host` | string | 按需 | 目标主机 |
| `port` | number | 否 | 端口（SSH 默认 22，Telnet 默认 23，ADB 默认 5555） |
| `user` | string | SSH | SSH 用户名 |
| `password` | string | SSH | SSH 密码 |
| `privateKey` | string | SSH | SSH 私钥内容（与 password 二选一） |
| `pty` | bool | 否 | 是否分配 PTY（默认 false，local 模式强制 false） |

```bash
# Local
{"reqId":"1","action":"connect","params":{"type":"local"}}
# → {"reqId":"1","type":"result","ok":true}

# SSH
{"reqId":"1","action":"connect","params":{"host":"192.168.1.100","user":"root","password":"secret"}}
# → {"reqId":"1","type":"result","ok":true}

# Telnet（telnet 协议强制有 PTY，输出始终含 raw+text）
{"reqId":"1","action":"connect","params":{"type":"telnet","host":"192.168.1.200","port":23}}
# → {"reqId":"1","type":"result","ok":true}
```

### close

关闭当前连接，清理所有资源（包括活跃的 GDB 和 Shell 会话）。

```bash
{"reqId":"2","action":"close"}
# → {"reqId":"2","type":"result","ok":true}
```

---

## 2. One-Shot 执行（exec）

执行一条命令，完成后返回。支持四种后端。

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `cmd` | string | 是 | 要执行的命令 |
| `pty` | bool | 否 | 是否分配 PTY（默认 false） |

**响应流：** `stdout` (0~N 条) → `stderr` (0~N 条) → `exit`

```bash
# 无 PTY（纯文本）
{"reqId":"1","action":"exec","params":{"cmd":"ls -la /tmp"}}
# → {"reqId":"1","type":"stdout","data":"total 8"}

# 有 PTY（SSH 模式下带颜色输出）
{"reqId":"1","action":"exec","params":{"cmd":"ls -la /tmp","pty":true}}
# → {"reqId":"1","type":"stdout","raw":"\u001b[34mtotal 8\u001b[0m","text":"total 8"}
```

---

## 3. GDB 交互式调试

维持常驻 `gdb` 进程，通过 `(gdb)` 提示符状态机实现一问一答。

### gdbStart

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `program` | string | 否 | 要调试的程序路径 |

支持后端：Local、SSH、Telnet。**ADB 不支持。**

```bash
{"reqId":"1","action":"gdbStart","params":{"program":"./a.out"}}
# → {"reqId":"1","type":"stdout","data":"Reading symbols from ./a.out..."}
# → {"reqId":"1","type":"result","ok":true,"data":"gdb started"}
# → {"reqId":"1","type":"exit","code":0}
```

### gdbCmd

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `cmd` | string | 是 | GDB 命令 |

```bash
{"reqId":"2","action":"gdbCmd","params":{"cmd":"bt full"}}
# → {"reqId":"2","type":"stdout","data":"#0  0x00007ffff7a..."}
# → {"reqId":"2","type":"exit","code":0}
```

### gdbExit

```bash
{"reqId":"3","action":"gdbExit"}
# → {"reqId":"3","type":"result","ok":true}
# → {"reqId":"3","type":"exit","code":0}
```

---

## 4. Shell 交互式会话

启动 Shell 进程，通过 push 模式实时推送输出。

### shellStart

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `shell` | string | 否 | Shell 路径，默认 `bash` |
| `cwd` | string | 否 | 工作目录 |
| `pty` | bool | 否 | 是否分配 PTY（默认 false） |

**注意：** `pty=true` 时输出包含 `raw` + `text` 双字段，`pty=false` 时仅 `data`。

```bash
# 无 PTY
{"reqId":"1","action":"shellStart","params":{"shell":"bash","cwd":"/tmp"}}
# → {"reqId":"1","type":"result","ok":true}
# → {"reqId":"1","type":"exit","code":0}

# 有 PTY（支持 top/vim 等终端程序）
{"reqId":"1","action":"shellStart","params":{"pty":true}}
```

### shellWrite

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `data` | string | 是 | 要写入的数据 |

**重要：** `data` 需要以 `\n` 结尾触发命令执行。`\r` 自动转换为 `\n`。

```bash
{"reqId":"2","action":"shellWrite","params":{"data":"ls -la\n"}}
# → {"reqId":"2","type":"result","ok":true}
# → {"reqId":"2","type":"exit","code":0}
```

### shellStop

```bash
{"reqId":"3","action":"shellStop"}
# → {"reqId":"3","type":"result","ok":true}
# → {"reqId":"3","type":"exit","code":0}
```

### Push 输出协议

Shell 的实时输出通过固定 `reqId: _shell_output` 推送：

**无 PTY：**
```json
{"reqId":"_shell_output","type":"stdout","data":"hello world\n"}
```

**有 PTY：**
```json
{"reqId":"_shell_output","type":"stdout","raw":"\u001b[32mhello world\u001b[0m\n","text":"hello world\n"}
```

Push 输出与请求-响应流交错出现，客户端通过 `reqId` 字段区分。

---

## 5. 完整示例

### 本地执行 + GDB 调试

```bash
echo '{"reqId":"1","action":"connect","params":{"type":"local"}}' | dish acp
echo '{"reqId":"2","action":"gdbStart","params":{"program":"./a.out"}}' | dish acp
echo '{"reqId":"3","action":"gdbCmd","params":{"cmd":"break main"}}' | dish acp
echo '{"reqId":"4","action":"gdbCmd","params":{"cmd":"run"}}' | dish acp
echo '{"reqId":"5","action":"gdbCmd","params":{"cmd":"bt"}}' | dish acp
echo '{"reqId":"6","action":"gdbExit"}' | dish acp
echo '{"reqId":"7","action":"close"}' | dish acp
```

### Shell 交互式会话

```bash
echo '{"reqId":"1","action":"connect","params":{"type":"local"}}' | dish acp
echo '{"reqId":"2","action":"shellStart","params":{}}' | dish acp
echo '{"reqId":"3","action":"shellWrite","params":{"data":"cd /tmp && pwd\n"}}' | dish acp
echo '{"reqId":"4","action":"shellWrite","params":{"data":"echo hello\n"}}' | dish acp
echo '{"reqId":"5","action":"shellStop"}' | dish acp
echo '{"reqId":"6","action":"close"}' | dish acp
```

---

## 6. 响应格式说明

所有动作的输出 `stdout`/`stderr` 事件格式由 PTY 参数决定：

| PTY | `stdout` 字段 | 用途 |
|-----|---------------|------|
| `false` | `data` | 纯文本，直接给 AI / 日志 |
| `true` | `raw` + `text` | `raw` 给终端渲染，`text` 给 AI / 日志 |

---

## 7. 架构

```
┌──────────┐  JSON (stdin)   ┌──────────┐
│  Client  │ ──────────────> │  dish  │
│ (AI /    │ <────────────── │  (Go)    │
│  KCode ) │  JSON (stdout)  │          │
└──────────┘                 └──────────┘
```

### 响应格式一致

无论 `exec`、`gdbCmd` 还是 shell push，`stdout` 格式完全一致：

```json
{"reqId":"...","type":"stdout","data":"..."}
// 或 PTY 模式下：
{"reqId":"...","type":"stdout","raw":"...","text":"..."}
```

上层渲染器无需区分来源。
