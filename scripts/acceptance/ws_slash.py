#!/usr/bin/env python3
"""Drive the gateway websocket chat as the web UI does, for acceptance.

Sends /mcp and prints the reply, so the session-scoped slash path (which the
gateway only exposes over the websocket) can be exercised without a browser.
Uses only the standard library: implements just enough of the WebSocket
client handshake and framing for one text exchange on a loopback connection.
"""
import base64
import hashlib
import json
import os
import socket
import struct
import sys

import os
TOKEN = os.environ.get("GATEWAY_TOKEN", "")
HOST_PORT = sys.argv[1]
SESSION = sys.argv[2]
TEXT = sys.argv[3] if len(sys.argv) > 3 else "/mcp"

host, port = HOST_PORT.split(":")
port = int(port)
key = base64.b64encode(os.urandom(16)).decode()

sock = socket.create_connection((host, port), timeout=20)
request = (
    "GET /ws/chat HTTP/1.1\r\n"
    f"Host: {host}:{port}\r\n"
    "Upgrade: websocket\r\n"
    "Connection: Upgrade\r\n"
    f"Sec-WebSocket-Key: {key}\r\n"
    "Sec-WebSocket-Version: 13\r\n"
    + (f"Authorization: Bearer {TOKEN}\r\n" if TOKEN else "")
    + "\r\n"
)
sock.sendall(request.encode())

# Read the handshake response.
buf = b""
while b"\r\n\r\n" not in buf:
    chunk = sock.recv(4096)
    if not chunk:
        sys.exit("connection closed during handshake")
    buf += chunk
head, rest = buf.split(b"\r\n\r\n", 1)
if b"101" not in head.split(b"\r\n")[0]:
    sys.exit("handshake failed: " + head.split(b"\r\n")[0].decode())


def send_text(payload):
    data = json.dumps(payload).encode()
    mask = os.urandom(4)
    masked = bytes(b ^ mask[i % 4] for i, b in enumerate(data))
    header = bytes([0x81])
    n = len(data)
    if n < 126:
        header += bytes([0x80 | n])
    elif n < 65536:
        header += bytes([0x80 | 126]) + struct.pack(">H", n)
    else:
        header += bytes([0x80 | 127]) + struct.pack(">Q", n)
    sock.sendall(header + mask + masked)


def read_frame():
    """Return one text payload, or None when the peer closes."""
    head = recv_exact(2)
    opcode = head[0] & 0x0F
    n = head[1] & 0x7F
    if n == 126:
        n = struct.unpack(">H", recv_exact(2))[0]
    elif n == 127:
        n = struct.unpack(">Q", recv_exact(8))[0]
    payload = recv_exact(n) if n else b""
    if opcode == 0x8:
        return None
    if opcode in (0x1, 0x2, 0x0):
        return payload.decode("utf-8", "replace")
    return ""


def recv_exact(n):
    out = b""
    while len(out) < n:
        chunk = sock.recv(n - len(out))
        if not chunk:
            raise ConnectionError("closed")
        out += chunk
    return out


send_text({"op": "start_run", "request_id": "accept-1", "message": {"content": TEXT}, "session_id": SESSION})

lines = []
try:
    while True:
        raw = read_frame()
        if raw is None:
            break
        if not raw:
            continue
        try:
            msg = json.loads(raw)
        except json.JSONDecodeError:
            continue
        op = msg.get("op") or msg.get("type") or ""
        payload = msg.get("data") or msg.get("message") or {}
        text = msg.get("text") if isinstance(msg.get("text"), str) else ""
        if not text and isinstance(payload, dict):
            text = payload.get("content") or payload.get("text") or ""
        elif not text and isinstance(payload, str):
            text = payload
        if not text and isinstance(msg.get("message"), str):
            text = msg["message"]
        if not text:
            text = msg.get("reply") if isinstance(msg.get("reply"), str) else ""
        if text:
            lines.append(text)
            if "MCP Servers" in text or "scope=" in text:
                break
except (ConnectionError, OSError):
    pass
finally:
    sock.close()

if lines:
    print("\n".join(lines))
else:
    print("NO-REPLY", file=sys.stderr)
    sys.exit(1)
