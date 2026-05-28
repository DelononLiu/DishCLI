# DishCLI

底层命令行执行引擎，通过 STDIN/STDOUT JSON 协议提供统一流式接口。支持**单次命令执行**和**交互式会话**（GDB 等）两种模式，底层可对接 SSH、Telnet、ADB、Local 四种连接类型。

## 协议

所有请求通过 STDIN 输入单行 JSON，响应通过 STDOUT 逐行流式输出。

### 通用字段

```json
{"reqId":"<请求ID>","action":"<动作>","params":{...}}
```

### 响应流式格式

每条响应独立一行，按顺序推送：

| type | 含义 |
|------|------|
| `stdout` | 标准输出行 |
| `stderr` | 标准错误行 |
| `result` | 操作结果（ok=true/false） |
| `exit` | 命令执行结束 |
| `error` | 错误信息 |

---

## 使用场景

### 1. 本地命令执行（one-shot）

```bash
echo '{"reqId":"1","action":"connect","params":{"type":"local"}}' | dishcli --json
echo '{"reqId":"2","action":"exec","params":{"cmd":"ls -la /tmp"}}' | dishcli --json
echo '{"reqId":"3","action":"close"}' | dishcli --json
```

流式输出每行独立推送，不会等命令结束后才一次性返回。

### 2. SSH 远程命令执行

```bash
echo '{"reqId":"1","action":"connect","params":{
  "host":"192.168.1.100","user":"root","password":"secret"
}}' | dishcli --json

echo '{"reqId":"2","action":"exec","params":{"cmd":"uname -a"}}' | dishcli --json
```

也支持密钥认证：将 `password` 替换为 `privateKey` 字段。

### 3. Telnet 执行

```bash
echo '{"reqId":"1","action":"connect","params":{
  "type":"telnet","host":"192.168.1.200","port":23
}}' | dishcli --json

echo '{"reqId":"2","action":"exec","params":{"cmd":"show running-config"}}' | dishcli --json
```

Telnet 模式下通过 500ms 空闲超时判断命令是否执行完毕。

### 4. ADB 执行

```bash
# USB 设备
echo '{"reqId":"1","action":"connect","params":{"type":"adb"}}' | dishcli --json
echo '{"reqId":"2","action":"exec","params":{"cmd":"dumpsys battery"}}' | dishcli --json

# 网络设备
echo '{"reqId":"1","action":"connect","params":{"type":"adb","host":"192.168.1.50"}}' | dishcli --json
```

### 5. GDB 交互式调试

GDB 模式下 DishCLI 内部维护常驻 gdb 进程，通过 `(gdb)` 提示符状态机实现一问一答的交互。

```bash
# 1. 连接（local 模式）
echo '{"reqId":"1","action":"connect","params":{"type":"local"}}' | dishcli --json

# 2. 启动 GDB 会话
echo '{"reqId":"2","action":"gdbStart","params":{"program":"./a.out"}}' | dishcli --json

# 3. 下发 GDB 命令
echo '{"reqId":"3","action":"gdbCmd","params":{"cmd":"bt full"}}' | dishcli --json
echo '{"reqId":"4","action":"gdbCmd","params":{"cmd":"info registers"}}' | dishcli --json
echo '{"reqId":"5","action":"gdbCmd","params":{"cmd":"frame 0"}}' | dishcli --json

# 4. 退出 GDB
echo '{"reqId":"6","action":"gdbExit"}' | dishcli --json
```

GDB 命令的输出也是逐行流式推送，与普通 exec 的 `stdout` 格式完全一致，上层渲染无需区分。

### 6. GDB 跨 SSH 远程调试

```bash
echo '{"reqId":"1","action":"connect","params":{"host":"remote-server","user":"dev","privateKey":"..."}}' | dishcli --json
echo '{"reqId":"2","action":"gdbStart","params":{"program":"/opt/app/bin/myapp"}}' | dishcli --json
echo '{"reqId":"3","action":"gdbCmd","params":{"cmd":"bt"}}' | dishcli --json
echo '{"reqId":"4","action":"gdbExit"}' | dishcli --json
```

GDB 进程运行在远程机器上，本地仅通过 SSH 通道转发 stdin/stdout，所有调试数据不出远程网络。

### 7. Shell 交互式会话

Shell 模式下 DishCLI 启动本地 bash（或指定 shell）进程，通过 push 方式实时推送输出，适合长时间运行的交互式命令（如 `top`、`python` 等）。

```bash
# 1. 连接（local 模式）
echo '{"reqId":"1","action":"connect","params":{"type":"local"}}' | dishcli --json

# 2. 启动 shell 会话
echo '{"reqId":"2","action":"shellStart","params":{"shell":"bash","cwd":"/tmp"}}' | dishcli --json

# 3. 下发命令（输出通过 reqId="_shell_output" 实时推送）
echo '{"reqId":"3","action":"shellWrite","params":{"data":"ls -la\n"}}' | dishcli --json

# 4. 停止 shell 会话
echo '{"reqId":"4","action":"shellStop"}' | dishcli --json
```

注意事项：
- Shell 输出通过 `reqId: _shell_output` 的 `stdout`/`stderr` 事件实时推送，无需轮询
- `shellWrite` 的 `data` 参数需要以 `\n` 结尾触发命令执行
- 当前仅支持 Local 模式

### 8. 会话中途退出与重开

```bash
echo '{"reqId":"1","action":"gdbStart","params":{}}' | dishcli --json
echo '{"reqId":"2","action":"gdbCmd","params":{"cmd":"quit"}}' | dishcli --json   # 进程自动清理
echo '{"reqId":"3","action":"gdbStart","params":{"program":"/bin/ls"}}' | dishcli --json  # 自动重开
```

`close` 动作也会自动清理所有活跃的 GDB 会话。

---

## 动作列表

| action | 用途 |
|--------|------|
| `connect` | 建立连接（SSH/Telnet/ADB/Local） |
| `exec` | 单次执行命令（one-shot） |
| `gdbStart` | 启动 GDB 交互式会话 |
| `gdbCmd` | 在活跃 GDB 会话中下发命令 |
| `gdbExit` | 退出 GDB 会话 |
| `shellStart` | 启动交互式 Shell 会话（local 模式） |
| `shellWrite` | 向 Shell 写入数据（params: `data`） |
| `shellStop` | 停止 Shell 会话 |
| `close` | 关闭连接，清理所有资源 |

## 连接类型

| type | 协议 | 底层实现 |
|------|------|---------|
| `local` | 本地 | `os/exec` |
| （默认/未指定） | SSH | `golang.org/x/crypto/ssh` |
| `telnet` | Telnet | `net.Dial` + Telnet negotiation |
| `adb` | ADB | `adb shell` |

## 构建

```bash
go build -o dishcli .
```
