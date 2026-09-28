#!/usr/bin/env python3
"""caveman-mode-tracker — forebrain UserPromptSubmit hook.

Reads BaseInput JSON from stdin (fields: prompt, transcript_path, session_id,
cwd, hook_event_name). Three responsibilities:

  1. /caveman-stats [args...]      → run caveman-stats.py and return a
                                     decision=block result with the formatted
                                     output as `reason`.
  2. /caveman <mode>                → write or clear the mode flag at
                                     $FOREBRAIN_HOME/state/.caveman-active.
                                     Also recognises natural-language toggles.
  3. mode flag active               → emit hookSpecificOutput.additionalContext
                                     so every user turn re-injects the
                                     caveman style rules.

Designed to fail silent: if anything goes wrong reading stdin / spawning the
stats script / writing the flag, we either emit nothing or a benign block.
The hook runtime ignores empty stdout.
"""
from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from pathlib import Path

VALID_MODES = {"lite", "full", "ultra", "wenyan", "wenyan-lite", "wenyan-full", "wenyan-ultra"}
INDEPENDENT_MODES = {"commit", "review", "compress"}
DEFAULT_MODE = "full"

ACTIVATE_RE = re.compile(
    r"\b(activate|enable|turn on|start|talk like)\b.*\bcaveman\b"
    r"|\bcaveman\b.*\b(mode|activate|enable|turn on|start)\b",
    re.IGNORECASE,
)
DEACTIVATE_RE = re.compile(
    r"\b(stop|disable|deactivate|turn off)\b.*\bcaveman\b"
    r"|\bcaveman\b.*\b(stop|disable|deactivate|turn off)\b"
    r"|\bnormal mode\b",
    re.IGNORECASE,
)
STATS_RE = re.compile(r"^\s*/caveman(?::caveman)?-stats(?:\s+(.*))?$", re.IGNORECASE)
CAVEMAN_CMD_RE = re.compile(r"^\s*/caveman(?::caveman)?(?:\s+(\S+))?\s*$", re.IGNORECASE)
SUBCMD_RE = re.compile(r"^\s*/caveman-(commit|review|compress)\b", re.IGNORECASE)


def forebrain_home() -> Path:
    return Path(os.environ.get("FOREBRAIN_HOME") or (Path.home() / ".forebrain"))


def flag_path(home: Path) -> Path:
    return home / "state" / ".caveman-active"


def write_flag(home: Path, mode: str) -> None:
    p = flag_path(home)
    try:
        p.parent.mkdir(parents=True, exist_ok=True)
        if p.is_symlink():
            return
        p.write_text(mode, encoding="utf-8")
    except OSError:
        pass


def clear_flag(home: Path) -> None:
    p = flag_path(home)
    try:
        if p.is_symlink():
            return
        p.unlink(missing_ok=True)
    except OSError:
        pass


def read_flag(home: Path) -> str | None:
    p = flag_path(home)
    try:
        if p.is_symlink():
            return None
        raw = p.read_text(encoding="utf-8").strip()
    except OSError:
        return None
    if raw in VALID_MODES:
        return raw
    return None


def run_stats(transcript: str | None, session_id: str | None, tail_args: list[str]) -> str:
    here = Path(__file__).resolve().parent
    stats = here / "caveman-stats.py"
    argv = [sys.executable, str(stats)]
    if transcript:
        argv.extend(["--transcript", transcript])
    if session_id:
        argv.extend(["--session-id", session_id])
    for t in tail_args:
        if t in ("--share", "--all"):
            argv.append(t)
    if "--since" in tail_args:
        i = tail_args.index("--since")
        if i + 1 < len(tail_args):
            argv.extend(["--since", tail_args[i + 1]])
    try:
        out = subprocess.run(
            argv, capture_output=True, text=True, timeout=5, check=False,
        )
        return (out.stdout or out.stderr or "").strip()
    except (subprocess.TimeoutExpired, OSError) as e:
        return f"caveman-stats: failed to run ({e})"


def emit(obj: dict) -> None:
    sys.stdout.write(json.dumps(obj, separators=(",", ":")))


def main() -> int:
    try:
        raw = sys.stdin.read()
        data = json.loads(raw) if raw.strip() else {}
    except (json.JSONDecodeError, OSError):
        return 0

    prompt = (data.get("prompt") or "").strip()
    transcript = data.get("transcript_path") or None
    session_id = data.get("session_id") or None
    home = forebrain_home()

    if not prompt:
        return 0

    if ACTIVATE_RE.search(prompt) and not DEACTIVATE_RE.search(prompt):
        write_flag(home, DEFAULT_MODE)

    m = STATS_RE.match(prompt)
    if m:
        tail = (m.group(1) or "").split()
        out = run_stats(transcript, session_id, tail)
        emit({"decision": "block", "reason": out})
        return 0

    sub = SUBCMD_RE.match(prompt)
    if sub:
        write_flag(home, sub.group(1).lower())
    else:
        cmd = CAVEMAN_CMD_RE.match(prompt)
        if cmd:
            arg = (cmd.group(1) or "").lower()
            if not arg:
                write_flag(home, DEFAULT_MODE)
            elif arg in ("off", "stop", "disable"):
                clear_flag(home)
            elif arg == "wenyan-full":
                write_flag(home, "wenyan")
            elif arg in VALID_MODES:
                write_flag(home, arg)

    if DEACTIVATE_RE.search(prompt):
        clear_flag(home)

    active = read_flag(home)
    if active and active not in INDEPENDENT_MODES:
        emit({
            "hookSpecificOutput": {
                "hookEventName": "UserPromptSubmit",
                "additionalContext": (
                    f"CAVEMAN MODE ACTIVE ({active}). "
                    "Drop articles/filler/pleasantries/hedging. Fragments OK. "
                    "Code/commits/security: write normal."
                ),
            }
        })
    return 0


if __name__ == "__main__":
    sys.exit(main())
