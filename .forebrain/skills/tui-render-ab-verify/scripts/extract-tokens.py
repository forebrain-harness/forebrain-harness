#!/usr/bin/env python3
"""Print the syntax-coloured runs of a `tmux capture-pane -p -e` dump.

    extract-tokens.py <capture.ansi> [substring ...]

Without substrings every non-blank line is printed. With them, only lines
containing any of them are — so one line of source code gives a compact view of
which colour each token class actually got.

Colours are labelled as `#rrggbb` for 24-bit SGR and `idx N` for the 256-colour
form, which is what a terminal without 24-bit support receives. A trailing
summary lists the distinct colours, and the closest pair among them: several
colours within a few RGB steps of each other means the highlighting has
collapsed into one tone.
"""

import re
import sys

SGR = re.compile(r"\x1b\[([0-9;]*)m")


def label(code):
    if code is None:
        return "default"
    if code.startswith("38;2;"):
        r, g, b = (int(v) for v in code.split(";")[2:5])
        return "#%02x%02x%02x" % (r, g, b)
    if code.startswith("38;5;"):
        return "idx %s" % code.split(";")[2]
    return code


def runs(line):
    """Split one captured line into (colour code or None, text) runs."""
    out, pos, colour = [], 0, None
    for m in SGR.finditer(line):
        text = line[pos:m.start()]
        if text:
            out.append((colour, text))
        code = m.group(1)
        # A bare/zero/39 reset returns to the terminal default foreground; the
        # capture nests SGR runs, so anything else is a new explicit colour.
        if code in ("", "0", "39"):
            colour = None
        elif code.startswith("38;2;") or code.startswith("38;5;"):
            colour = code
        pos = m.end()
    if line[pos:]:
        out.append((colour, line[pos:]))
    return out


# Entries 16-255 of the xterm palette: the 6x6x6 cube plus the grey ramp.
# Indices 0-15 are whatever the user's terminal theme sets, so they are left out
# of the distance comparison rather than guessed at.
_CUBE = (0, 95, 135, 175, 215, 255)
_PALETTE = [(r, g, b) for r in _CUBE for g in _CUBE for b in _CUBE]
_PALETTE += [(8 + 10 * n,) * 3 for n in range(24)]


def rgb_of(code):
    """The colour a run actually resolves to, for 24-bit and 256-colour alike.

    The 256 path is the one real terminals and their screenshots use, so the
    distance check has to work there too.
    """
    if code.startswith("38;2;"):
        return tuple(int(v) for v in code.split(";")[2:5])
    if code.startswith("38;5;"):
        idx = int(code.split(";")[2])
        return _PALETTE[idx - 16] if 16 <= idx <= 255 else None
    return None


def main():
    if len(sys.argv) < 2:
        print(__doc__.strip(), file=sys.stderr)
        return 2
    path, needles = sys.argv[1], sys.argv[2:]
    distinct, per_line = [], []
    with open(path, encoding="utf-8", errors="replace") as fh:
        for n, line in enumerate(fh, 1):
            line = line.rstrip("\n")
            # Match against the text as it reads on screen: a phrase like
            # "return f.Kind" spans several colour runs, so matching the raw
            # line would fail on the escapes between them.
            if needles and not any(nd in SGR.sub("", line) for nd in needles):
                continue
            got = [(c, t) for c, t in runs(line) if t.strip() and c]
            if not got:
                continue
            print("--- line %d" % n)
            for code, text in got:
                print("    %-10s %r" % (label(code), text.strip()))
                if code not in distinct:
                    distinct.append(code)
            per_line.append(n)

    print("\ndistinct colours: %d" % len(distinct))
    for code in distinct:
        print("    %s" % label(code))
    rgbs = [c for c in (rgb_of(x) for x in distinct) if c]
    if rgbs:
        # Spread is the second half of the judgement. A flattened palette keeps
        # every token at one brightness and one weak hue, so both the luminance
        # range and the strongest saturation collapse — which a distance check
        # alone can miss on the coarse 256-colour palette.
        lums = sorted(relative_luminance(c) for c in rgbs)
        sats = sorted(saturation(c) for c in rgbs)
        print("luminance: %.3f..%.3f (range %.3f)" % (lums[0], lums[-1], lums[-1] - lums[0]))
        print("strongest saturation: %.2f" % sats[-1])
    if len(rgbs) > 1:
        best = min(
            (sum((a[k] - b[k]) ** 2 for k in range(3)) ** 0.5, a, b)
            for i, a in enumerate(rgbs)
            for b in rgbs[i + 1:]
        )
        d, a, b = best
        print("closest pair: %s vs %s = %.0f RGB steps" % (label_color(a), label_color(b), d))
    return 0


def relative_luminance(rgb):
    def lin(c):
        v = c / 255
        return v / 12.92 if v <= 0.04045 else ((v + 0.055) / 1.055) ** 2.4

    r, g, b = (lin(c) for c in rgb)
    return 0.2126 * r + 0.7152 * g + 0.0722 * b


def saturation(rgb):
    mx, mn = max(rgb), min(rgb)
    return 0.0 if mx == 0 else (mx - mn) / mx


def label_color(rgb):
    return "#%02x%02x%02x" % rgb


if __name__ == "__main__":
    sys.exit(main())
