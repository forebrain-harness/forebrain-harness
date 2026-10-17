#!/usr/bin/env python3
"""Run forebrain live in a macOS Terminal.app window and press real keys in it,
for bugs that need both the real terminal and the real keyboard path: an input
method's marked text (pinyin being composed), which neither tmux nor a replay of
the painter's bytes ever produces.

  term_live.py open  <dir> [cols] [rows]   forebrain from driver.sh's build, home
                                           and project, in a new window
  term_live.py probe <dir> <row> <col> [cols] [rows]
                                           a known screen instead of forebrain:
                                           every row a letter on a coloured
                                           background, the cursor parked at
                                           <row>;<col>, nothing ever written again
  term_live.py send  <dir> <text>          bytes straight to the program, with
                                           Python escapes ('\\x16' is ctrl+v);
                                           no input method sees them
  term_live.py keys  <dir> <key>...        real key presses through System
                                           Events, which the input method sees:
                                           a word of letters (xiufu), a key name
                                           (space esc return tab delete shift) or
                                           a raw macOS key code
  term_live.py shot  <dir> <png>           screenshot of the window
  term_live.py contents <dir>              Terminal.app's own screen, as text
  term_live.py bytes <dir> [seconds]       what the program wrote in the last
                                           N seconds (default 5), one write a line
  term_live.py paint <dir> <command>       probe only: fill | row R | cup R C
  term_live.py close <dir>                 quit the program; its window closes

<dir> holds the run: the window id, out.bin (every byte the program wrote, as
the terminal received it), out.idx (offset and time of each write), in.log
(every input byte, from the keyboard or from `send`) and the in.fifo `send`
writes to. forebrain runs inside a transparent pty proxy that passes every byte
through unchanged and keeps those logs.

`open` needs the fake provider `driver.sh serve <mode>` starts, and honours the
driver's FOREBRAIN_RUN_WORK and FOREBRAIN_RUN_PORT. The window is addressed only
by the id `do script` returns, never "front window"; `keys` brings that window
to the front and checks it is the front window before every press.
"""
import codecs
import fcntl
import os
import pty
import re
import select
import shlex
import signal
import socket
import subprocess
import sys
import termios
import time
import tty
import unicodedata
from typing import NoReturn

SELF = os.path.abspath(__file__)
WORK = os.environ.get("FOREBRAIN_RUN_WORK") or os.path.join(os.environ.get("TMPDIR", "/tmp"), "forebrain-run")
PORT = int(os.environ.get("FOREBRAIN_RUN_PORT") or "8731")
BIN = os.path.join(WORK, "forebrain")
HOME_DIR = os.path.join(WORK, "home")
PROJ = os.path.join(WORK, "proj")
ESCAPES = re.compile(rb"\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b.")


def die(msg) -> NoReturn:
    sys.stderr.write("term_live: %s\n" % msg)
    sys.exit(1)


def osa(script, timeout=20):
    try:
        p = subprocess.run(["osascript", "-e", script], capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        die("osascript timed out — a permission dialog is probably waiting on screen: %s" % script)
    if p.returncode != 0:
        die("osascript failed: %s" % p.stderr.strip())
    return p.stdout.strip()


def path(d, name):
    return os.path.join(d, name)


def wid_of(d) -> str:
    try:
        return open(path(d, "wid")).read().strip()
    except OSError:
        die("no window recorded in %s; run `open` or `probe` first" % d)


def fresh(d):
    os.makedirs(d, exist_ok=True)
    for n in ("wid", "kind", "out.bin", "out.idx", "in.log", "in.fifo", "cmd", "proxy.err"):
        if os.path.exists(path(d, n)):
            os.remove(path(d, n))


def new_window(d, kind, cmd):
    """Opens a Terminal.app window running cmd under exec, so it closes when
    the program exits, and records the window's id."""
    ref = osa('tell application "Terminal" to do script "%s"' % cmd.replace("\\", "\\\\").replace('"', '\\"'))
    wid = ref.rsplit("window id", 1)[-1].strip() if "window id" in ref else ""
    if not wid.isdigit():
        die("no window id from do script: %r" % ref)
    open(path(d, "wid"), "w").write(wid)
    open(path(d, "kind"), "w").write(kind)
    return wid


def screen_text(d):
    try:
        data = open(path(d, "out.bin"), "rb").read()
    except OSError:
        return ""
    return ESCAPES.sub(b"", data).decode("utf-8", "replace")


def cmd_open(d, cols=141, rows=30):
    if not os.access(BIN, os.X_OK):
        die("no binary at %s; run driver.sh build" % BIN)
    if not os.path.exists(path(HOME_DIR, "forebrain.yaml")):
        die("no config in %s; run driver.sh serve <mode> first" % HOME_DIR)
    try:
        socket.create_connection(("127.0.0.1", PORT), timeout=1).close()
    except OSError:
        die("nothing listens on port %d; run driver.sh serve <mode> first" % PORT)
    d = os.path.abspath(d)
    fresh(d)
    env = "FAKE_LLM_KEY=sk-local-fake FOREBRAIN_HOME=%s" % shlex.quote(HOME_DIR)
    cmd = "cd %s && %s exec python3 %s _proxy %s %d %d -- %s 2>%s" % (
        shlex.quote(PROJ), env, shlex.quote(SELF), shlex.quote(d), int(rows), int(cols),
        shlex.quote(BIN), shlex.quote(path(d, "proxy.err")))
    wid = new_window(d, "forebrain", cmd)
    # First launch in a new project asks for trust; the page opens on Quit, so
    # the answer is Down then Enter, sent once — the same as driver.sh does.
    trusted = False
    deadline = time.time() + 40
    while time.time() < deadline:
        text = screen_text(d)
        if "Trust this directory?" in text and not trusted:
            cmd_send(d, "\\x1b[B\\r")
            trusted = True
        elif "commands" in text and (trusted or "Trust this directory?" not in text):
            print("window %s: forebrain %dx%d, logs in %s" % (wid, int(cols), int(rows), d))
            return
        time.sleep(0.2)
    die("forebrain never reached the composer in window %s; see %s" % (wid, path(d, "proxy.err")))


def cmd_probe(d, row, col, cols=40, rows=12):
    d = os.path.abspath(d)
    fresh(d)
    cmd = "exec python3 %s _probe %s %d %d %d %d" % (
        shlex.quote(SELF), shlex.quote(d), int(row), int(col), int(rows), int(cols))
    wid = new_window(d, "probe", cmd)
    deadline = time.time() + 10
    while not os.path.exists(path(d, "in.log")):
        if time.time() > deadline:
            die("the probe never started in window %s" % wid)
        time.sleep(0.1)
    time.sleep(0.3)
    print("window %s: probe %dx%d, cursor at %d;%d" % (wid, int(cols), int(rows), int(row), int(col)))


def cmd_send(d, text):
    data = codecs.escape_decode(text.encode("utf-8"))[0]
    with open(path(d, "in.fifo"), "wb", buffering=0) as f:
        f.write(data)


# macOS virtual key codes: what System Events presses, and what an input
# method reads (it ignores the text a `keystroke` carries).
LETTER_CODES = {
    "a": 0, "b": 11, "c": 8, "d": 2, "e": 14, "f": 3, "g": 5, "h": 4, "i": 34,
    "j": 38, "k": 40, "l": 37, "m": 46, "n": 45, "o": 31, "p": 35, "q": 12,
    "r": 15, "s": 1, "t": 17, "u": 32, "v": 9, "w": 13, "x": 7, "y": 16, "z": 6,
}
NAMED_CODES = {"return": 36, "tab": 48, "space": 49, "delete": 51, "esc": 53, "shift": 56}


def key_codes(keys):
    codes = []
    for key in keys:
        if key.isdigit():
            codes.append(key)
        elif key in NAMED_CODES:
            codes.append(str(NAMED_CODES[key]))
        elif key and all(c in LETTER_CODES for c in key):
            codes.extend(str(LETTER_CODES[c]) for c in key)
        else:
            die("unknown key %r: use lowercase letters, %s or a key code" % (key, "/".join(NAMED_CODES)))
    return codes


def cmd_keys(d, *keys):
    codes = key_codes(keys)
    if not codes:
        die("keys takes letters (xiufu), key names (space esc …) or key codes")
    wid = wid_of(d)
    osa('tell application "Terminal" to set index of window id %s to 1' % wid)
    osa('tell application "Terminal" to activate')
    time.sleep(0.4)
    front = osa('tell application "Terminal" to get id of front window')
    app = osa('tell application "System Events" to get name of first process whose frontmost is true', timeout=10)
    if front != wid or app != "Terminal":
        die("window %s is not in front (front window %s of %s); no key sent" % (wid, front, app))
    # Shift pressed in the same call as the keys after it acts as their
    # modifier (shift then x types X), so it goes in a call of its own.
    batch = []
    for code in codes + [None]:
        if code is None or code == str(NAMED_CODES["shift"]):
            if batch:
                osa('tell application "System Events" to key code {%s}' % ", ".join(batch))
                batch = []
            if code is not None:
                osa('tell application "System Events" to key code %s' % code)
                time.sleep(0.2)
            continue
        batch.append(code)


def cmd_shot(d, png):
    subprocess.run(["screencapture", "-x", "-o", "-l", wid_of(d), png], check=True)
    print(png)


def display_width(s):
    return sum(2 if unicodedata.east_asian_width(c) in "WF" else 1 for c in s)


def cmd_contents(d):
    text = osa('tell application "Terminal" to get contents of tab 1 of window id %s' % wid_of(d))
    for line in text.split("\n"):
        print("%4d|%s" % (display_width(line), line))


def cmd_bytes(d, seconds=5):
    data = open(path(d, "out.bin"), "rb").read()
    idx = [(int(a), float(b)) for a, b in (l.split() for l in open(path(d, "out.idx")))]
    since = time.time() - float(seconds)
    for i, (off, t) in enumerate(idx):
        if t >= since:
            end = idx[i + 1][0] if i + 1 < len(idx) else len(data)
            print("%.3f %r" % (t, data[off:end]))


def cmd_paint(d, *words):
    if open(path(d, "kind")).read() != "probe":
        die("paint drives the probe only")
    with open(path(d, "cmd"), "a") as f:
        f.write(" ".join(words) + "\n")
    time.sleep(0.3)


def cmd_close(d):
    wid = wid_of(d)
    if open(path(d, "kind")).read() == "probe":
        cmd_paint(d, "quit")
    else:
        cmd_send(d, "\\x03")
        time.sleep(0.4)
        cmd_send(d, "\\x03")
    for _ in range(100):
        if osa('tell application "Terminal" to exists window id %s' % wid) != "true":
            print("window %s closed" % wid)
            return
        time.sleep(0.1)
    print("note: window %s is still open" % wid)


def resize_self(rows, cols):
    """Asks the terminal this runs in for rows x cols, before anything reads
    the size."""
    os.write(1, b"\x1b[8;%d;%dt" % (rows, cols))
    time.sleep(0.6)


def run_proxy(d, rows, cols, argv):
    """Runs inside the window: the program on a pty of its own, every byte
    passed through unchanged both ways and logged."""
    resize_self(rows, cols)
    fifo = path(d, "in.fifo")
    if not os.path.exists(fifo):
        os.mkfifo(fifo)
    pid, master = pty.fork()
    if pid == 0:
        os.execvp(argv[0], argv)

    def follow_size(*_):
        try:
            fcntl.ioctl(master, termios.TIOCSWINSZ, fcntl.ioctl(0, termios.TIOCGWINSZ, b"\0" * 8))
            os.kill(pid, signal.SIGWINCH)
        except OSError:
            pass

    follow_size()
    signal.signal(signal.SIGWINCH, follow_size)
    old = termios.tcgetattr(0)
    tty.setraw(0)
    out = open(path(d, "out.bin"), "ab")
    idx = open(path(d, "out.idx"), "a")
    inlog = open(path(d, "in.log"), "a")
    ffd = os.open(fifo, os.O_RDONLY | os.O_NONBLOCK)
    keep = os.open(fifo, os.O_WRONLY)  # so the fifo never reads EOF between sends
    try:
        while True:
            try:
                ready, _, _ = select.select([0, master, ffd], [], [])
            except InterruptedError:
                continue
            if master in ready:
                try:
                    data = os.read(master, 65536)
                except OSError:
                    break
                if not data:
                    break
                idx.write("%d %.6f\n" % (out.tell(), time.time()))
                out.write(data)
                out.flush()
                idx.flush()
                os.write(1, data)
            for fd, source in ((0, "kbd"), (ffd, "send")):
                if fd in ready:
                    data = os.read(fd, 65536)
                    if data:
                        inlog.write("%.6f %s %r\n" % (time.time(), source, data))
                        inlog.flush()
                        os.write(master, data)
    finally:
        termios.tcsetattr(0, termios.TCSADRAIN, old)
        os.close(keep)


def run_probe(d, row, col, rows, cols):
    """Runs inside the window: a known screen, then only input logging and the
    commands `paint` appends, so any later change to Terminal.app's screen is
    the terminal's own doing."""
    resize_self(rows, cols)
    cols, rows = os.get_terminal_size()

    def paint_row(r):
        ch = chr(ord("A") + (r - 1) % 26).encode()
        os.write(1, b"\x1b[%d;1H\x1b[48;5;238m\x1b[38;5;252m" % r + ch * cols + b"\x1b[0m")

    def fill():
        os.write(1, b"\x1b[?1049h\x1b[2J")
        for r in range(1, rows + 1):
            paint_row(r)

    old = termios.tcgetattr(0)
    tty.setraw(0)
    log = open(path(d, "in.log"), "a")
    try:
        fill()
        os.write(1, b"\x1b[%d;%dH" % (row, col))
        log.write("size %dx%d cursor %d;%d\n" % (cols, rows, row, col))
        log.flush()
        done = 0
        while True:
            ready, _, _ = select.select([0], [], [], 0.1)
            if ready:
                log.write("%.6f kbd %r\n" % (time.time(), os.read(0, 4096)))
                log.flush()
            if not os.path.exists(path(d, "cmd")):
                continue
            lines = open(path(d, "cmd")).read().splitlines()
            for line in lines[done:]:
                words = line.split()
                if words == ["quit"]:
                    return
                if words == ["fill"]:
                    fill()
                elif len(words) == 2 and words[0] == "row":
                    paint_row(int(words[1]))
                elif len(words) == 3 and words[0] == "cup":
                    os.write(1, b"\x1b[%d;%dH" % (int(words[1]), int(words[2])))
                log.write("paint %s\n" % line)
                log.flush()
            done = len(lines)
    finally:
        os.write(1, b"\x1b[?1049l")
        termios.tcsetattr(0, termios.TCSADRAIN, old)


def main(argv):
    if not argv:
        sys.stderr.write(__doc__ or "")
        sys.exit(2)
    cmd, args = argv[0], argv[1:]
    if cmd == "_proxy":
        sep = args.index("--")
        run_proxy(args[0], int(args[1]), int(args[2]), args[sep + 1:])
    elif cmd == "_probe":
        run_probe(args[0], *(int(a) for a in args[1:5]))
    elif cmd in ("open", "probe", "send", "keys", "shot", "contents", "bytes", "paint", "close") and args:
        globals()["cmd_" + cmd](*args)
    else:
        sys.stderr.write(__doc__ or "")
        sys.exit(2)


if __name__ == "__main__":
    main(sys.argv[1:])
