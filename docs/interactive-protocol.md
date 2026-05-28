# DishCLI 交互式协议文档

## 概述

DishCLI 通过 STDIN/STDOUT 单行 JSON 协议提供统一流式接口。支持四种交互模式：

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
| `stdout` | 标准输出行 | `reqId`, `type`, `data` |
| `stderr` | 错误输出行 | `reqId`, `type`, `data` |
| `result` | 操作结果 | `reqId`, `type`, `ok` |
| `exit` | 命令/会话结束 | `reqId`, `type`, `code` |
| `error` | 错误信息 | `reqId`, `type`, `msg` |

---

## 1. 连接管理

### connect

建立到目标的连接，所有后续 `exec` 和 `gdb*` 动作依赖此连接。

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `type` | string | 否 | 连接类型：`local` / `telnet` / `adb` / (默认 SSH) |
| `host` | string | 按需 | 目标主机 |
| `port` | number | 否 | 端口（SSH 默认 22，Telnet 默认 23，ADB 默认 5555） |
| `user` | string | SSH | SSH 用户名 |
| `password` | string | SSH | SSH 密码 |
| `privateKey` | string | SSH | SSH 私钥内容（与 password 二选一） |

**示例：**

```bash
# Local
{"reqId":"1","action":"connect","params":{"type":"local"}}
# → {"reqId":"1","type":"result","ok":true}

# SSH
{"reqId":"1","action":"connect","params":{"host":"192.168.1.100","user":"root","password":"secret"}}
# → {"reqId":"1","type":"result","ok":true}

# Telnet
{"reqId":"1","action":"connect","params":{"type":"telnet","host":"192.168.1.200","port":23}}
# → {"reqId":"1","type":"result","ok":true}

# ADB (USB)
{"reqId":"1","action":"connect","params":{"type":"adb"}}
# → {"reqId":"1","type":"result","ok":true}

# ADB (Network)
{"reqId":"1","action":"connect","params":{"type":"adb","host":"192.168.1.50"}}
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

执行一条命令，等待完成后返回。支持 Local / SSH / Telnet / ADB 四种后端。

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `cmd` | string | 是 | 要执行的命令 |

**响应流：** `stdout` (0~N 条) → `stderr` (0~N 条) → `exit`

```bash
{"reqId":"1","action":"exec","params":{"cmd":"ls -la /tmp"}}
# → {"reqId":"1","type":"stdout","data":"total 8"}
# → {"reqId":"1","type":"stdout","data":"drwxrwxrwt  2 root root 4096 ..."}
# → {"reqId":"1","type":"exit","code":0}
```

Telnet 模式下通过 500ms 空闲超时判断命令完成，其余模式通过进程/会话退出信号。

---

## 3. GDB 交互式调试

维护常驻 `gdb` 进程，通过 `(gdb)` 提示符状态机实现一问一答的交互。

### gdbStart

启动 GDB 会话。

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `program` | string | 否 | 要调试的程序路径 |

支持后端：Local、SSH、Telnet。**ADB 不支持。**

**响应流：** `stdout` (GDB 启动信息) → `result` → `exit`

```bash
{"reqId":"1","action":"gdbStart","params":{"program":"./a.out"}}
# → {"reqId":"1","type":"stdout","data":"Reading symbols from ./a.out..."}
# → {"reqId":"1","type":"result","ok":true,"data":"gdb started"}
# → {"reqId":"1","type":"exit","code":0}
```

### gdbCmd

向活跃 GDB 会话发送命令。

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `cmd` | string | 是 | GDB 命令 |

**响应流：** `stdout` (命令输出) → `exit`

```bash
{"reqId":"2","action":"gdbCmd","params":{"cmd":"bt full"}}
# → {"reqId":"2","type":"stdout","data":"#0  0x00007ffff7a..."}
# → {"reqId":"2","type":"stdout","data":"#1  0x0000555555..."}
# → {"reqId":"2","type":"exit","code":0}
```

### gdbExit

退出 GDB 会话。发送 `quit` 命令，等待最多 3 秒后强制清理。

**响应流：** `result` → `exit`

```bash
{"reqId":"3","action":"gdbExit"}
# → {"reqId":"3","type":"result","ok":true}
# → {"reqId":"3","type":"exit","code":0}
```

**GDB 会话生命周期：**
- `close` 动作会自动清理活跃的 GDB 会话
- 再次调用 `gdbStart` 会自动关闭已有会话再开启新的
- GDB 内部的 `quit` 命令效果等同于 `gdbExit`

---

## 4. Shell 交互式会话

启动本地 bash（或指定 shell）进程，通过 **push 模式**实时推送输出，适合长时间运行的交互式命令。

与 GDB 不同，Shell 会话的 stdout/stderr 输出通过后台协程主动推送，**无需轮询**。

### shellStart

启动 Shell 会话。

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `shell` | string | 否 | Shell 路径，默认 `bash` |
| `cwd` | string | 否 | 工作目录 |

**注意：当前仅支持 Local 模式。** Shell 以 `--norc --noprofile -i` 参数启动。

**响应流：** `result` → `exit`

```bash
{"reqId":"1","action":"shellStart","params":{"shell":"bash","cwd":"/tmp"}}
# → {"reqId":"1","type":"result","ok":true}
# → {"reqId":"1","type":"exit","code":0}
```

### shellWrite

向 Shell 进程的 STDIN 写入数据。

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `data` | string | 是 | 要写入的数据 |

**重要：** `data` 需要以 `\n` 结尾来触发命令执行。`\r` 会自动转换为 `\n`。

**响应流：** `result` → `exit`

```bash
{"reqId":"2","action":"shellWrite","params":{"data":"ls -la\n"}}
# → {"reqId":"2","type":"result","ok":true}
# → {"reqId":"2","type":"exit","code":0}
```

### shellStop

停止 Shell 会话，杀死 Shell 进程。

**响应流：** `result` → `exit`

```bash
{"reqId":"3","action":"shellStop"}
# → {"reqId":"3","type":"result","ok":true}
# → {"reqId":"3","type":"exit","code":0}
```

### Push 输出协议

Shell 会话的实时输出通过 **固定 reqId `_shell_output`** 推送：

| type | 含义 |
|------|------|
| `stdout` | Shell 的标准输出 |
| `stderr` | Shell 的标准错误/提示符 |

```json
{"reqId":"_shell_output","type":"stderr","data":"bash-5.1$ "}
{"reqId":"_shell_output","type":"stdout","data":"hello world\n"}
```

Push 输出与请求-响应的回复流**交错出现**，客户端应通过 `reqId` 字段区分：
- 请求的响应：`reqId` 与请求一致
- Shell 实时输出：`reqId` 为固定值 `_shell_output`

### Shell 会话生命周期

```
shellStart → [shellWrite → (push输出)]* → shellStop
     ↑                                          |
     └───────────── 再次 shellStart ────────────┘
```

- `close` 动作自动停止活跃的 Shell 会话
- 再次调用 `shellStart` 会自动停止已有会话再开启新的
- `exit` 事件表示写入操作已确认，**不是** Shell 进程退出

---

## 5. 完整示例

### 本地执行 + GDB 调试

```bash
echo '{"reqId":"1","action":"connect","params":{"type":"local"}}' | dishcli --json
echo '{"reqId":"2","action":"gdbStart","params":{"program":"./a.out"}}' | dishcli --json
echo '{"reqId":"3","action":"gdbCmd","params":{"cmd":"break main"}}' | dishcli --json
echo '{"reqId":"4","action":"gdbCmd","params":{"cmd":"run"}}' | dishcli --json
echo '{"reqId":"5","action":"gdbCmd","params":{"cmd":"bt"}}' | dishcli --json
echo '{"reqId":"6","action":"gdbExit"}' | dishcli --json
echo '{"reqId":"7","action":"close"}' | dishcli --json
```

### Shell 交互式会话

```bash
echo '{"reqId":"1","action":"connect","params":{"type":"local"}}' | dishcli --json
echo '{"reqId":"2","action":"shellStart","params":{}}' | dishcli --json
echo '{"reqId":"3","action":"shellWrite","params":{"data":"cd /tmp && pwd\n"}}' | dishcli --json
echo '{"reqId":"4","action":"shellWrite","params":{"data":"echo hello\n"}}' | dishcli --json
echo '{"reqId":"5","action":"shellStop"}' | dishcli --json
echo '{"reqId":"6","action":"close"}' | dishcli --json
```

> 注意：上述 `echo` 示例中 `\n` 在 JSON 字符串内需要写成字面 `\\n`。实际开发中建议使用 JSON 库序列化。

---

## 6. 二次开发指引

### 架构概览

```
┌──────────┐  JSON (stdin)   ┌──────────┐
│  Client  │ ──────────────> │  DishCLI │
│ (KCode/  │ <────────────── │ (Go)     │
│  Python/ │  JSON (stdout)  │          │
│  等)     │                 └──────────┘
└──────────┘
```

### 核心文件

| 文件 | 职责 |
|------|------|
| `main.go` | 入口 + JSON 请求路由 |
| `client.go` | 连接管理（Local/SSH/Telnet/ADB） |
| `oneshot.go` | One-Shot 命令执行（四种后端） |
| `interactive.go` | GDB 会话 + Shell 会话 |

### 响应格式一致

所有动作的输出（无论是 `exec`、`gdbCmd` 还是 shell push）的 `stdout`/`stderr` 事件格式完全一致：

```json
{"reqId":"...","type":"stdout","data":"..."}
```

上层渲染器无需区分来源。

### 状态机说明

**GDB 会话** 使用基于字节的 `(gdb)` 提示符检测：

```
开始 → 读取字节 → 检测 (gdb) 提示符 → 输出行 → 等待下一个命令
```

检测到 `(gdb) ` 后返回 `exit` 表示命令执行完毕。

**Shell 会话** 使用后台协程连续读取 stdout/stderr：

```
shellStart → 启动进程 → [协程: 读 stdout → push + buffer]
                     → [协程: 读 stderr → push]
shellWrite → 写入 stdin
shellStop  → cancel context + kill 进程
```

### 添加新的交互模式

要添加新的交互动作，需要：

1. 在 `main.go` 的 `switch req.Action` 中添加新路由
2. 在 `interactive.go`（或新文件）中实现处理函数
3. 在 `handleExecAction` 中如果涉及到 GDB 类型的请求路由
4. 在 `client.go` 的 `closeClientLocked` 中添加清理逻辑
5. 在 `--help` 和 `README.md` 中添加文档

### 测试方式

```bash
# 构建
go build -o dishcli .

# 直接用 echo 测试（注意 \\n 转义）
echo '{"reqId":"1","action":"connect","params":{"type":"local"}}' | ./dishcli --json

# 使用 Python REPL（手动交互测试）
python3 repl.py

# 使用 Python 脚本自动化测试
python3 -c "
import subprocess, json
proc = subprocess.Popen(['./dishcli', '--json'],
    stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
proc.stdin.write(json.dumps({'reqId':'1','action':'connect','params':{'type':'local'}}) + '\n')
proc.stdin.write(json.dumps({'reqId':'2','action':'exec','params':{'cmd':'echo hi'}}) + '\n')
proc.stdin.close()
print(proc.stdout.read())
"
```
