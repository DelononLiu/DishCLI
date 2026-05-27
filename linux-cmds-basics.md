# find, ls, awk 入门指南

## 1. ls — 文件列表

`ls` 列出目录内容。

```bash
ls                    # 列出当前目录文件
ls /path              # 列出指定目录
ls -l                 # 长格式（权限、大小、时间）
ls -a                 # 显示隐藏文件（以 . 开头的文件）
ls -lh                # 人类可读大小（KB/MB/GB）
ls -lt                # 按时间排序（最新在前）
ls -lS                # 按大小排序（最大在前）
ls *.txt              # 通配符：列出所有 .txt 文件
ls -d */              # 只列出目录
```

`-l` 输出示例：
```
-rw-r--r-- 1 user user 1234 May 27 10:00 file.txt
```
各列含义：权限 | 硬链接数 | 所有者 | 组 | 大小 | 修改时间 | 文件名

---

## 2. find — 文件搜索

`find` 在目录树中搜索文件。

```bash
find . -name "*.go"           # 按名称查找
find . -iname "*.Go"          # 忽略大小写
find . -type f                # 只找文件
find . -type d                # 只找目录
find . -size +1M              # 大于 1MB 的文件
find . -size -10k             # 小于 10KB 的文件
find . -mtime -7              # 7 天内修改过的文件
find . -mtime +30             # 30 天前修改过的文件
find . -name "*.log" -delete  # 查找并删除
find . -name "*.go" -exec wc -l {} \;   # 对每个结果执行命令
```

常见 `-type`：
- `f` 普通文件
- `d` 目录
- `l` 符号链接

常见时间条件：
- `-mtime` 修改时间
- `-atime` 访问时间
- `-ctime` 状态改变时间
- `-mmin -30` 30分钟内修改过

---

## 3. awk — 文本处理

`awk` 按行读取、按列分割处理文本。

### 基本结构
```bash
awk 'pattern { action }' file
```

### 字段引用
```bash
awk '{ print $1, $3 }' file      # 打印第1、3列
awk '{ print $NF }' file         # 打印最后一列
awk '{ print NR, $0 }' file      # 打印行号和整行
```

### 内置变量
- `$0` 整行
- `$1, $2, ...` 第 n 列
- `NF` 列数
- `NR` 当前行号
- `FS` 字段分隔符（默认空格/Tab）

### 常用示例
```bash
ls -l | awk '{ print $5, $9 }'          # 打印文件大小和名称
awk -F: '{ print $1 }' /etc/passwd      # 以 : 分隔，打印第一列
awk '$5 > 1000 { print }' file          # 第5列大于1000的行
awk 'NR > 1 { print }' file             # 跳过第一行
awk '{ sum += $1 } END { print sum }'   # 对第一列求和
```

### 完整示例：分析 ls -l 输出
```bash
ls -l | awk 'NR>1 { print $5, $9 }'     # 跳过 total 行，打印大小和文件名
ls -l | awk '$1 ~ /^d/ { print $9 }'   # 只列出目录
ls -l | awk '{ s+=$5 } END { print "Total: " s " bytes" }'  # 计算总大小
```

---

## 组合使用

```bash
# 找最大文件
find . -type f -exec ls -lh {} \; | awk '{ print $5, $9 }' | sort -rh

# 统计每种扩展名的文件数
find . -type f | awk -F. '{ ext=$(NF); count[ext]++ } END { for (e in count) print count[e], e }'

# 找到近期修改的大文件
find . -type f -mtime -7 -size +100k
```

每个命令都有 `man <cmd>` 或 `tldr <cmd>`（需安装 tldr）查看更多用法。

---

## 5. 综合练习

以下是在 `/tmp/cmd-lab` 中执行的实战演示（也可在你的项目目录中尝试）。

### 练习1：find + wc — 统计所有 txt 文件行数
```bash
find . -name "*.txt" -exec wc -l {} +
```
输出：
```
  0 ./subdir/file_a.txt
  3 ./passwd-sample.txt
  4 ./fruits.txt
  7 total
```

### 练习2：ls + awk — 提取文件名和大小
```bash
ls -lh | awk 'NR>1 {print $5, $9}'
```
输出：
```
33K chunk_1.bin
88K chunk_2.bin
104K chunk_3.bin
54 fruits.txt
139 passwd-sample.txt
100 subdir
```

### 练习3：find + ls -ld — 查看找到的目录详情
```bash
find . -type d -exec ls -ld {} \;
```
输出：
```
drwxr-xr-x 3 long2015 long2015 180 May 27 08:45 .
drwxr-xr-x 2 long2015 long2015 100 May 27 08:45 ./subdir
```

### 练习4：awk 统计每种扩展名的文件数
```bash
find . -type f | awk -F. '{ext=$(NF); count[ext]++} END {for (e in count) print count[e], e}'
```
输出：
```
1 log
1 md
3 bin
1 hidden-file
3 txt
```

### 练习5：文件总大小
```bash
find . -type f -exec ls -l {} \; | awk '{sum+=$5} END {print "Total size:", sum, "bytes (" sum/1024 " KB)"}'
```
输出：
```
Total size: 230593 bytes (225.188 KB)
```

### 练习6：ls -l 用 awk 只显示目录
```bash
ls -l | awk '$1 ~ /^d/ {print $9}'
```
输出：
```
subdir
```

---

## 6. 速查表

| 命令 | 用途 | 常用组合 |
|------|------|----------|
| `ls -la` | 列出所有文件含隐藏文件 | 最常用 |
| `ls -lh` | 可读大小格式 | 配合 `-l` |
| `ls -lt` | 按修改时间排序 | 配合 `-l` |
| `ls -lS` | 按文件大小排序 | 配合 `-l` |
| `ls -d */` | 只列目录 | — |
| `find . -name "*.go"` | 按名称递归查找 | 加 `-iname` 忽略大小写 |
| `find . -type f` | 只找文件 | `-type d` 只找目录 |
| `find . -size +1M` | 找大于1MB的文件 | `-size -10k` 小于10KB |
| `find . -mtime -7` | 7天内修改过的文件 | `-mmin -30` 30分钟内 |
| `find . -exec cmd {} +` | 对结果执行命令 | 用 `+` 批量执行，`\;`逐个执行 |
| `awk '{print $1}'` | 打印第一列 | `$NF` 最后一列 |
| `awk -F: '{print $1}'` | 指定分隔符为冒号 | 常用语 /etc/passwd |
| `awk 'NR>1'` | 跳过第一行 | `NR==1` 只取第一行 |
| `awk '$1 ~ /^a/'` | 正则匹配第一列以a开头 | `!~` 不匹配 |
| `awk '{sum+=$1} END {print sum}'` | 对某列求和 | 可扩展到平均值等 |
