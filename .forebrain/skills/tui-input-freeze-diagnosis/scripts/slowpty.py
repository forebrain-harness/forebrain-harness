#!/usr/bin/env python3
"""Spawn forebrain on a pty with a capped drain rate; drive input; report.

The drain-rate cap plays the role of a real terminal (iTerm2/Terminal.app)
that pauses reading its pty while it re-renders. tmux and test buffers drain
instantly, so drain-dependent freezes never reproduce there.

Usage:
  slowpty.py <binary> <FOREBRAIN_HOME> <projdir> <DRAIN> <in.jsonl> <out.log>

  DRAIN   max bytes/second drained from the pty; 0 = uncapped (measure only)
  in.jsonl  one command per line: {"t": <seconds to wait first>,
            "b": "<base64 bytes to write to the pty>"}
  out.log raw terminal output; a JSON summary goes to stdout, child stderr
          to <out.log>.err

Every input command records a mark: how many bytes the app had written
before it ran, and when. A stall shows up as marks whose byte counter stops
advancing while input keeps being delivered.
"""
import base64
import fcntl
import json
import os
import os.path
import pty
import select
import struct
import subprocess
import sys
import termios
import threading
import time


def main():
    if len(sys.argv) != 7:
        sys.exit(__doc__)
    binary, home, projdir, drain, infile, outlog = sys.argv[1:7]
    drain = int(drain)

    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 40, 120, 0, 0))
    env = dict(os.environ)
    env['FOREBRAIN_HOME'] = home
    env['TERM'] = 'xterm-256color'
    err = open(outlog + '.err', 'w')
    child = subprocess.Popen([binary], stdin=slave, stdout=slave, stderr=err,
                             env=env, cwd=projdir, preexec_fn=os.setsid)
    os.close(slave)

    stats = {'bytes': 0, 't0': time.time(), 'marks': []}
    out = open(outlog, 'wb')
    stop = threading.Event()

    def drainer():
        # Token bucket over a 1s window: read eagerly, then sleep only when
        # this window's bytes exceed what DRAIN allows for its elapsed time.
        window_start = time.time()
        window_bytes = 0
        while not stop.is_set():
            r, _, _ = select.select([master], [], [], 0.05)
            if r:
                try:
                    data = os.read(master, 65536)
                except OSError:
                    return
                if not data:
                    return
                stats['bytes'] += len(data)
                out.write(data)
                out.flush()
                if drain > 0:
                    window_bytes += len(data)
                    while True:
                        elapsed = time.time() - window_start
                        if window_bytes <= elapsed * drain:
                            break
                        if time.time() - window_start >= 1.0:
                            break
                        time.sleep(min(0.05,
                                       (window_bytes - elapsed * drain) / drain))
            if time.time() - window_start >= 1.0:
                window_start = time.time()
                window_bytes = 0

    t = threading.Thread(target=drainer, daemon=True)
    t.start()

    for line in open(infile):
        cmd = json.loads(line)
        time.sleep(cmd.get('t', 0))
        stats['marks'].append({
            'before': stats['bytes'],
            'at': round(time.time() - stats['t0'], 6),
        })
        os.write(master, base64.b64decode(cmd['b']))

    time.sleep(2)
    stop.set()
    t.join(timeout=1)
    alive = child.poll() is None
    print(json.dumps({
        'total_bytes': stats['bytes'],
        'alive': alive,
        'marks': stats['marks'],
    }))
    child.terminate()
    out.close()


if __name__ == '__main__':
    main()
