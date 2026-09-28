#!/usr/bin/env python3
"""Row-by-row byte audit of `tmux capture-pane -p -e -N` output.

Usage:
    tmux capture-pane -t SESSION -p -e -N | row-bytes.py [expected_width]

Prints, for every non-blank row: the visible column count, whether a
trailing erase-in-line (\\x1b[K) is present, the SGR state active while the
last painted cell was written, and the raw tail bytes.

Audit rules encoded here:
  - A row whose visible width equals the terminal width AND that carries
    \\x1b[K is the deferred-wrap hazard: on terminals that keep the wrap
    pending (macOS Terminal.app) the EL erases the last painted cell.
    Rows are flagged `FULLW+EL` against expected_width when given.
  - `last-sgr` is the SGR state at the last painted cell, tracked by ONE
    continuous state machine over the whole stream: tmux capture re-emits
    SGR incrementally across rows (attributes still active at the end of a
    row are not repeated on the next), so per-row tracking falsely reports
    missing backgrounds from the second row on. A card/rule row whose last
    cell shows no `bg=...` relies on EL for its edge fill, which loses the
    background on terminals without back-color-erase.

Pass `-e` (escape sequences) and `-N` (keep trailing spaces) to
capture-pane; without `-N` tmux trims trailing spaces and every pad-filled
row looks short.
"""
import re
import sys

SGR = re.compile(r"\x1b\[([0-9;]*)m")
CSI = re.compile(r"\x1b\[[0-9;?]*[A-Za-z]")
EL = "\x1b[K"


def apply_sgr(state, seq):
    params = SGR.match(seq).group(1)
    codes = [p for p in params.split(";") if p != ""] or ["0"]
    i = 0
    while i < len(codes):
        code = codes[i]
        if code == "0":
            state.clear()
        elif code == "49":
            bg = [c for c in state if c.startswith("bg=")]
            for c in bg:
                state.discard(c)
        elif code in ("48", "38") and i + 2 < len(codes) and codes[i + 1] == "5":
            key = ("bg=" if code == "48" else "fg=") + codes[i + 2]
            old = [c for c in state if c.startswith(key[:3])]
            for c in old:
                state.discard(c)
            state.add(key)
            i += 2
        elif code in ("48", "38") and i + 4 < len(codes) and codes[i + 1] == "2":
            key = ("bg=" if code == "48" else "fg=") + "rgb(" + ",".join(codes[i + 2:i + 5]) + ")"
            old = [c for c in state if c.startswith(key[:3])]
            for c in old:
                state.discard(c)
            state.add(key)
            i += 4
        else:
            state.add(code)
        i += 1


def scan(state, raw):
    """Run one raw row through the state machine.

    Returns the SGR state snapshot taken while the last visible cell was
    written, and advances `state` to the end of the row.
    """
    last_snapshot = None
    i = 0
    while i < len(raw):
        m = SGR.match(raw, i)
        if m:
            apply_sgr(state, m.group(0))
            i = m.end()
            continue
        if raw[i] == "\x1b":
            m = CSI.match(raw, i)
            i = m.end() if m else i + 1
            continue
        last_snapshot = set(state)
        i += 1
    return last_snapshot


def main():
    expect = int(sys.argv[1]) if len(sys.argv) > 1 else None
    state = set()
    for i, raw in enumerate(sys.stdin.read().split("\n")):
        plain = SGR.sub("", raw).replace(EL, "")
        if not plain.strip():
            scan(state, raw)
            continue
        last = scan(state, raw)
        cols = len(plain)
        flags = []
        if EL in raw:
            if expect is not None and cols >= expect:
                flags.append("FULLW+EL(deferred-wrap hazard)")
            else:
                flags.append("el")
        print(f"row {i:3d} cols={cols:3d} last-sgr[{' '.join(sorted(last))}] "
              f"{' '.join(flags)} tail={raw[-24:]!r}")
        if expect is not None and cols != expect:
            print(f"      ^ short of {expect} columns by {expect - cols}")


if __name__ == "__main__":
    main()
