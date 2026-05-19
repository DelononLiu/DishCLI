#!/usr/bin/env python3
import sys, os, json, subprocess, shlex, threading, queue

HISTFILE = os.path.expanduser("~/.dishcli_repl_history")


def main():
    args = sys.argv[1:] or ["./dishcli"]
    if "--json" not in args:
        args.append("--json")

    proc = subprocess.Popen(
        args, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
        stderr=subprocess.PIPE, text=True, bufsize=1,
    )

    _setup_readline()

    req_id = 0

    def send(obj):
        nonlocal req_id
        req_id += 1
        obj["reqId"] = str(req_id)
        line = json.dumps(obj, ensure_ascii=False)
        print(f"\033[33m>>> {line}\033[0m")
        proc.stdin.write(line + "\n")
        proc.stdin.flush()

    _start_output_pump(proc)

    print("dishcli REPL — /help for commands, ^D to exit")
    while True:
        try:
            line = input("repl> ")
        except (EOFError, KeyboardInterrupt):
            print()
            break
        if not line:
            continue

        parts = shlex.split(line)
        cmd = parts[0]

        if cmd == "/exit":
            break
        elif cmd == "/help":
            print(
                "  /connect [host] [user] [password]  connect\n"
                "  /exec <cmd>                        execute command\n"
                "  /close                             close connection\n"
                "  /raw <json>                        send raw JSON\n"
                "  /help /exit"
            )
        elif cmd == "/connect":
            host = parts[1] if len(parts) > 1 else "127.0.0.1"
            user = parts[2] if len(parts) > 2 else "long2015"
            password = parts[3] if len(parts) > 3 else ""
            send({"action": "connect", "params": {"host": host, "user": user, "password": password}})
        elif cmd == "/exec":
            if len(parts) < 2:
                print("usage: /exec <command>")
                continue
            send({"action": "exec", "params": {"cmd": parts[1]}})
        elif cmd == "/close":
            send({"action": "close"})
        elif cmd == "/raw":
            raw = " ".join(parts[1:])
            print(f"\033[33m>>> {raw}\033[0m")
            proc.stdin.write(raw + "\n")
            proc.stdin.flush()
        else:
            print(f"unknown: {cmd}  (/help)")

    proc.stdin.close()
    proc.wait()


def _setup_readline():
    try:
        import readline
        try:
            readline.read_history_file(HISTFILE)
        except FileNotFoundError:
            pass
        readline.set_history_length(500)
    except ImportError:
        pass


def _start_output_pump(proc):
    q = queue.Queue()
    stop = [False]

    def _reader():
        for line in proc.stdout:
            q.put(line)
        stop[0] = True

    threading.Thread(target=_reader, daemon=True).start()

    def _pump():
        while True:
            try:
                line = q.get(timeout=0.2)
            except queue.Empty:
                if stop[0]:
                    break
                continue
            try:
                obj = json.loads(line)
                t = obj.get("type")
                if t == "result":
                    c = "32" if obj.get("ok") else "31"
                    print(f"\033[{c}m<<< {json.dumps(obj)}\033[0m")
                elif t == "stdout":
                    print(f"     {obj.get('data', '')}")
                elif t == "stderr":
                    print(f"\033[31m     {obj.get('data', '')}\033[0m")
                elif t == "exit":
                    print(f"\033[90m<<< exit code {obj.get('code')}\033[0m")
                elif t == "error":
                    print(f"\033[31m<<< error: {obj.get('msg')}\033[0m")
                else:
                    print(f"<<< {json.dumps(obj)}")
            except json.JSONDecodeError:
                print(f"<<< {line.rstrip()}")
        # drain remaining
        while True:
            try:
                line = q.get_nowait()
                print(f"<<< {line.rstrip()}")
            except queue.Empty:
                break

    threading.Thread(target=_pump, daemon=True).start()


if __name__ == "__main__":
    main()
