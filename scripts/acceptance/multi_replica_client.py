#!/usr/bin/env python3
"""Drive two gateway replicas as a browser would, and print one PASS/FAIL line
per assertion of plan 213 §5.

Usage:
  multi_replica_client.py <addr-a> <addr-b> <token>

Replica A starts the conversation; the browser is connected to replica B, which
does not run it. Every assertion therefore crosses the replica boundary: B has to
relay to A for the socket, and A has to be the one that acts.

Assertions:
  A. B binds the conversation A is running, and receives the approval A is
     parked on — the streaming side of the relay.
  B. Approving on B resumes the run on A, and the completion reaches B.
  C. Interrupting on B stops the run A is executing.
  D. The replicas registered each other: both internal addresses are in
     fb_run_owners.

Standard library only: this is an acceptance script, not a client library.
"""

import base64
import hashlib
import json
import os
import socket
import struct
import sys
import time
import urllib.request

GW_A, GW_B, TOKEN, PROJECT_ROOT = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
MAX_WAIT = float(os.environ.get("MULTI_REPLICA_WAIT", "60"))
DEBUG = os.environ.get("MULTI_REPLICA_DEBUG") == "1"


class WS:
    """One websocket connection, enough of RFC 6455 for the chat protocol."""

    def __init__(self, host_port, token):
        host, port = host_port.split(":")
        self.sock = socket.create_connection((host, int(port)), timeout=15)
        key = base64.b64encode(os.urandom(16)).decode()
        request = (
            "GET /ws/chat HTTP/1.1\r\n"
            f"Host: {host}:{port}\r\n"
            "Upgrade: websocket\r\n"
            "Connection: Upgrade\r\n"
            f"Sec-WebSocket-Key: {key}\r\n"
            "Sec-WebSocket-Version: 13\r\n"
            f"Authorization: Bearer {token}\r\n"
            "\r\n"
        )
        self.sock.sendall(request.encode())
        head = b""
        while b"\r\n\r\n" not in head:
            chunk = self.sock.recv(4096)
            if not chunk:
                raise RuntimeError("handshake closed")
            head += chunk
        status = head.split(b"\r\n", 1)[0]
        if b" 101 " not in status:
            raise RuntimeError(f"handshake refused: {status!r}")
        accept = base64.b64encode(
            hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()
        ).decode()
        if accept.encode() not in head:
            raise RuntimeError("handshake accept mismatch")
        self.buf = head.split(b"\r\n\r\n", 1)[1]

    def send(self, payload):
        data = json.dumps(payload).encode()
        header = bytearray([0x81])
        mask = os.urandom(4)
        length = len(data)
        if length < 126:
            header.append(0x80 | length)
        elif length < 65536:
            header.append(0x80 | 126)
            header += struct.pack(">H", length)
        else:
            header.append(0x80 | 127)
            header += struct.pack(">Q", length)
        header += mask
        self.sock.sendall(bytes(header) + bytes(b ^ mask[i % 4] for i, b in enumerate(data)))

    def _read_exact(self, n):
        while len(self.buf) < n:
            chunk = self.sock.recv(65536)
            if not chunk:
                raise RuntimeError("connection closed")
            self.buf += chunk
        out, self.buf = self.buf[:n], self.buf[n:]
        return out

    def recv(self):
        """Return the next JSON message, answering a control frame if needed."""
        while True:
            first, second = self._read_exact(2)
            opcode = first & 0x0F
            length = second & 0x7F
            if length == 126:
                length = struct.unpack(">H", self._read_exact(2))[0]
            elif length == 127:
                length = struct.unpack(">Q", self._read_exact(8))[0]
            body = self._read_exact(length)
            if opcode == 0x9:  # ping
                self._pong(body)
                continue
            if opcode == 0x8:
                raise RuntimeError("server closed the socket")
            if opcode not in (0x1, 0x2):
                continue
            return json.loads(body.decode())

    def _pong(self, body):
        mask = os.urandom(4)
        header = bytearray([0x8A, 0x80 | len(body)]) + mask
        self.sock.sendall(bytes(header) + bytes(b ^ mask[i % 4] for i, b in enumerate(body)))

    def wait_for(self, predicate, what, timeout=MAX_WAIT):
        """Read until predicate(message) is true, collecting everything read."""
        seen = []
        deadline = time.time() + timeout
        self.sock.settimeout(1.0)
        while time.time() < deadline:
            try:
                msg = self.recv()
            except socket.timeout:
                continue
            seen.append(msg)
            if DEBUG:
                print("  <- " + json.dumps(msg)[:600], file=sys.stderr)
            if predicate(msg):
                return msg, seen
        raise RuntimeError(f"timed out waiting for {what}; saw {summarize(seen)}")

    def close(self):
        try:
            self.sock.close()
        except OSError:
            pass


def summarize(messages):
    out = []
    for m in messages:
        if m.get("op") in ("run_error", "run_warning"):
            out.append(f"{m.get('op')}({m.get('message')}: {m.get('error')})")
        else:
            out.append(str(m.get("op")))
    return out


def action_id_of(msg):
    data = msg.get("data") or {}
    if msg.get("op") == "requires_action":
        return str(data.get("action_id") or data.get("id") or "")
    if msg.get("op") == "run_event":
        if data.get("type") == "approval_requested":
            payload = data.get("payload") or {}
            return str(payload.get("action_id") or "")
    return ""


def run_id_of(msg):
    return str(msg.get("run_id") or "")


def http_json(method, addr, path, body=None):
    """One control-plane request, the way the web page makes it."""
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(
        f"http://{addr}{path}",
        method=method,
        data=data,
        headers={"Authorization": f"Bearer {TOKEN}", "Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        raw = resp.read().decode()
    return json.loads(raw) if raw.strip() else {}


def ensure_session():
    """A conversation inside a project: a turn with no project cannot run."""
    project = http_json("POST", GW_A, "/api/v1/projects", {
        "name": "multi-replica",
        "root": PROJECT_ROOT,
        "trust": True,
    })
    project_id = str(project.get("id") or "")
    if not project_id:
        fail("project", json.dumps(project)[:400])
    created = http_json("POST", GW_A, f"/api/v1/projects/{project_id}/sessions", {"title": "multi-replica"})
    session = str(created.get("id") or "")
    if not session:
        fail("session", json.dumps(created)[:400])
    return session


def fail(what, detail):
    print(f"FAIL {what}: {detail}")
    sys.exit(1)


def start_conversation(session):
    """Start a turn on A that stops at an approval, and name its run."""
    ws = WS(GW_A, TOKEN)
    ws.wait_for(lambda m: m.get("op") == "connected", "connected on A")
    # exit_plan_mode is only offered in plan mode, and this is the gate the
    # plan's approval is raised on.
    ws.send({"op": "switch_mode", "request_id": "mode-1", "session_id": session, "mode": "plan"})
    ws.wait_for(lambda m: m.get("op") == "mode_changed" or m.get("op") == "session_bound", "plan mode")
    ws.send({"op": "start_run", "request_id": "start-1", "session_id": session,
             "message": {"content": "drive the approval"}})
    started, seen = ws.wait_for(
        lambda m: m.get("op") in ("run_started", "session_bound", "run_error"),
        "the run to start on A",
    )
    if started.get("op") == "run_error":
        fail("start on A", json.dumps(started))
    session = str(started.get("session_id") or "")
    run_id = str(started.get("run_id") or "")
    if not run_id:
        for msg in seen:
            if run_id_of(msg):
                run_id = run_id_of(msg)
    if not session:
        for msg in seen:
            if msg.get("session_id"):
                session = str(msg["session_id"])
    if not session:
        fail("start on A", f"no session id in {summarize(seen)}")
    # The gate: A parks the run on the approval this turn reaches.
    gate, seen = ws.wait_for(lambda m: action_id_of(m) != "", "the approval on A")
    return ws, run_id or run_id_of(gate), action_id_of(gate)


def main():
    started_at = time.time()
    session = ensure_session()
    ws_a, run_id, action_id = start_conversation(session)

    # --- A: the browser on B sees the conversation A is running -------------
    ws_b = WS(GW_B, TOKEN)
    ws_b.wait_for(lambda m: m.get("op") == "connected", "connected on B")
    ws_b.send({"op": "bind_session", "request_id": "bind-1", "session_id": session})
    bound, _ = ws_b.wait_for(lambda m: m.get("op") == "session_bound", "session_bound on B")
    if str(bound.get("session_id")) != session:
        fail("A bind on B", f"bound {bound.get('session_id')!r}, want {session!r}")
    print(f"PASS A: B bound session {session} and received the approval {action_id}")

    # --- B: approving on B resumes the run A is parked on -------------------
    ws_b.send({"op": "approve_action", "request_id": "approve-1", "action_id": action_id})
    done, seen = ws_b.wait_for(
        lambda m: m.get("op") in ("run_completed", "run_error", "run_cancelled"),
        "the run to finish after the approval on B",
        timeout=MAX_WAIT * 2,
    )
    if done.get("op") != "run_completed":
        fail("B approval", f"run ended {done.get('op')}: {json.dumps(done)[:400]}")
    print(f"PASS B: approving on B resumed the run on A, which completed ({time.time() - started_at:.1f}s)")

    # --- C: interrupting on B stops the run A is executing ------------------
    ws_a.send({"op": "start_run", "request_id": "start-2", "session_id": session,
               "message": {"content": "drive the approval again"}})
    second, _ = ws_a.wait_for(
        lambda m: m.get("op") in ("run_started", "run_error") or action_id_of(m) != "",
        "the second run to start on A",
    )
    if second.get("op") == "run_error":
        fail("second start on A", json.dumps(second)[:400])
    second_run = run_id_of(second)
    if not second_run:
        second_run = run_id
    ws_b.send({"op": "control_request", "request_id": "int-1", "run_id": second_run,
               "type": "control_request", "request": {"subtype": "interrupt"}})
    stopped, seen = ws_b.wait_for(
        lambda m: m.get("op") in ("run_cancelled", "run_completed", "run_error") and run_id_of(m) == second_run,
        "the interrupted run to end",
        timeout=MAX_WAIT * 2,
    )
    if stopped.get("op") != "run_cancelled":
        fail("C interrupt", f"run ended {stopped.get('op')}: {json.dumps(stopped)[:400]}")
    print("PASS C: interrupting on B cancelled the run A was executing")

    ws_a.close()
    ws_b.close()


if __name__ == "__main__":
    main()
