#!/usr/bin/env python3
"""Replay the viewport painter's real bytes in macOS Terminal.app and screenshot
every step, for rendering bugs the tmux driver cannot see.

tmux re-renders the pane from its own cell grid, so anything that lives in
pixels rather than cells — a glyph's ink reaching past its row, a stray dot left
after a scroll, a notch in a card border — never shows in `driver.sh screen`.
This plays a recorded byte stream straight into a real Terminal.app window and
screenshots it, so old and new painter code can be compared pixel for pixel.

  term_replay.py run  <dir> [rows] [cols]   replay <dir>/NNN.bin then ref.bin,
                                            screenshot each to <dir>/<step>.png
  term_replay.py diff <dir> <a> <b>         compare <dir>/<a>.png with <b>.png

<dir> holds one file per step, written by a temporary Go test (see SKILL.md):
NNN.bin is everything the renderer wrote during that step, ref.bin a forced full
repaint of the final state. rows/cols default to 24/80 and must match the size
the dump was rendered at.

The window is addressed only by the id `do script` returns — never "front
window", because other Terminal windows may be running live sessions. The
replay runs under `exec`, so its window closes by itself when it finishes.
"""
import os
import subprocess
import sys
import time

SELF = os.path.abspath(__file__)
READY_TIMEOUT = 15.0


def die(msg):
    sys.stderr.write("term_replay: %s\n" % msg)
    sys.exit(1)


def steps(d):
    names = sorted(n[:-4] for n in os.listdir(d) if n.endswith(".bin") and n != "ref.bin")
    if not names:
        die("no NNN.bin steps in %s" % d)
    if os.path.exists(os.path.join(d, "ref.bin")):
        names.append("ref")
    return names


def clear_handshake(d):
    for n in os.listdir(d):
        if n.startswith("ready-") or n.startswith("go-"):
            os.remove(os.path.join(d, n))


def play(d, rows, cols):
    """Runs inside the Terminal.app window: write each step, then wait for the
    outer `run` to screenshot it before writing the next."""
    out = sys.stdout.buffer
    out.write(b"\x1b[8;%d;%dt" % (rows, cols))  # size the window to the dump
    out.flush()
    time.sleep(0.6)
    for name in steps(d):
        with open(os.path.join(d, name + ".bin"), "rb") as f:
            out.write(f.read())
        # The test renderer drives the hardware caret, whose blink would make
        # two screenshots of the same frame differ; hide it for the capture.
        out.write(b"\x1b[?25l")
        out.flush()
        time.sleep(0.15)
        open(os.path.join(d, "ready-" + name), "w").close()
        go = os.path.join(d, "go-" + name)
        while not os.path.exists(go):
            time.sleep(0.05)
    out.write(b"\x1b[r\x1b[0m\x1b[?1006l\x1b[?1003l\x1b[?1002l\x1b[?1000l\x1b[?25h\x1b[?1049l")
    out.flush()


def osa(script):
    return subprocess.run(["osascript", "-e", script], capture_output=True, text=True).stdout.strip()


def run(d, rows, cols):
    d = os.path.abspath(d)
    names = steps(d)
    clear_handshake(d)
    for n in names:
        p = os.path.join(d, n + ".png")
        if os.path.exists(p):
            os.remove(p)
    cmd = "exec python3 '%s' play '%s' %d %d" % (SELF, d, rows, cols)
    ref = osa('tell application "Terminal" to do script "%s"' % cmd.replace('"', '\\"'))
    # `do script` answers "tab 1 of window id N"; that id is the only window
    # this tool may touch.
    wid = ref.rsplit("window id", 1)[-1].strip() if "window id" in ref else ""
    if not wid.isdigit():
        die("no window id from do script: %r" % ref)
    print("window %s" % wid)
    try:
        for n in names:
            ready = os.path.join(d, "ready-" + n)
            deadline = time.time() + READY_TIMEOUT
            while not os.path.exists(ready):
                if time.time() > deadline:
                    die("the replay never reached step %s (window %s)" % (n, wid))
                time.sleep(0.05)
            time.sleep(0.8)  # let Terminal.app present the step
            subprocess.run(["screencapture", "-x", "-o", "-l", wid, os.path.join(d, n + ".png")], check=True)
            open(os.path.join(d, "go-" + n), "w").close()
    finally:
        # Unblock the replay whatever happened, so it exits and its window closes.
        for n in names:
            open(os.path.join(d, "go-" + n), "w").close()
    for _ in range(50):
        if osa('tell application "Terminal" to exists window id %s' % wid) != "true":
            break
        time.sleep(0.1)
    else:
        print("note: window %s is still open" % wid)
    clear_handshake(d)
    print("%d screenshots in %s" % (len(names), d))


def diff(d, a, b):
    try:
        from PIL import Image
    except ImportError:
        die("diff needs Pillow (python3 -c 'import PIL' must succeed)")
    ia = Image.open(os.path.join(d, a + ".png")).convert("RGB")
    ib = Image.open(os.path.join(d, b + ".png")).convert("RGB")
    if ia.size != ib.size:
        die("sizes differ: %s %s vs %s %s" % (a, ia.size, b, ib.size))
    w, h = ia.size
    bg = ib.getpixel((w - 20, h // 2))  # right margin: never painted
    pa, pb = ia.load(), ib.load()
    stray, other = [], []
    for y in range(h):
        for x in range(w):
            ca, cb = pa[x, y], pb[x, y]
            if sum(abs(i - j) for i, j in zip(ca, cb)) <= 40:
                continue
            # Ink in <a> where <b> shows bare background is "stray": something
            # left on screen that a fresh paint does not draw.
            (stray if sum(abs(i - j) for i, j in zip(cb, bg)) <= 20 else other).append((x, y))

    def box(ps):
        if not ps:
            return None
        return (min(p[0] for p in ps), min(p[1] for p in ps), max(p[0] for p in ps), max(p[1] for p in ps))

    print("%s vs %s: stray ink %d px %s, other differences %d px %s"
          % (a, b, len(stray), box(stray), len(other), box(other)))
    bb = box(stray) or box(other)
    if bb:
        x0, y0, x1, y1 = bb
        crop = ia.crop((max(0, x0 - 120), max(0, y0 - 80), min(w, x1 + 360), min(h, y1 + 80)))
        crop = crop.resize((crop.size[0] * 2, crop.size[1] * 2), Image.NEAREST)
        path = os.path.join(d, "%s-vs-%s.png" % (a, b))
        crop.save(path)
        print("zoomed crop of %s: %s" % (a, path))


def main(argv):
    if len(argv) >= 2 and argv[0] in ("run", "play"):
        rows = int(argv[2]) if len(argv) > 2 else 24
        cols = int(argv[3]) if len(argv) > 3 else 80
        (run if argv[0] == "run" else play)(argv[1], rows, cols)
    elif len(argv) == 4 and argv[0] == "diff":
        diff(argv[1], argv[2], argv[3])
    else:
        sys.stderr.write(__doc__)
        sys.exit(2)


if __name__ == "__main__":
    main(sys.argv[1:])
