#!/usr/bin/env python3
"""A controllable local reverse proxy for real-model acceptance runs.

It sits between forebrain and a real provider so a test can cut the network on
demand, which a live provider will not do on request. One process, one control
file:

    flaky_proxy.py <listen_port> <upstream_base_url> <control_file>

Every request reads the control file once and decides:

    pass           forward the request and stream the response back, chunk by
                   chunk, unbuffered
    drop-subagent  if the request body's system prompt carries the
                   general-purpose subagent's opening line, forward a few
                   response bytes and then abort the connection (a stream cut
                   mid-flight); every other request is forwarded normally
    drop-all       abort every request the same way

Anything else is treated as `pass`.

It listens on 127.0.0.1 only and never logs request bodies or headers, so a
provider key passing through it cannot end up in a log file. The upstream base
URL's path is kept and the client's path is appended, so a client pointed at
`http://127.0.0.1:<port>` reaches `<upstream_base_url>/chat/completions`.
"""

import http.client
import http.server
import json
import socket
import sys
import urllib.parse

LISTEN_PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 8743
UPSTREAM = sys.argv[2] if len(sys.argv) > 2 else "https://open.bigmodel.cn/api/coding/paas/v4"
CONTROL_FILE = sys.argv[3] if len(sys.argv) > 3 else "/tmp/flaky_proxy.control"

# The general-purpose subagent's system prompt leads with this line (see
# pkg/agent/subagent_defs.go); a request carrying it is the subagent's own.
SUBAGENT_SYSTEM_MARK = "You are a general-purpose subagent"

# Hop-by-hop headers are not forwarded: the proxy owns framing and the
# connection, and a stale Content-Length would desync the response.
HOP_BY_HOP = {
    "host", "content-length", "transfer-encoding", "connection",
    "proxy-connection", "keep-alive", "te", "trailer", "upgrade", "expect",
}

_parts = urllib.parse.urlsplit(UPSTREAM)
UPSTREAM_HOST = _parts.hostname
UPSTREAM_PORT = _parts.port or (443 if _parts.scheme == "https" else 80)
UPSTREAM_TLS = _parts.scheme == "https"
UPSTREAM_BASE_PATH = _parts.path.rstrip("/")


def control_value():
    """The current control value, read fresh for every request."""
    try:
        with open(CONTROL_FILE, "r", encoding="utf-8", errors="replace") as f:
            return f.read().strip() or "pass"
    except OSError:
        return "pass"


def read_body(handler):
    """The request body, decoding chunked framing when there is no length."""
    length = handler.headers.get("Content-Length")
    if length is not None:
        return handler.rfile.read(int(length))
    if (handler.headers.get("Transfer-Encoding") or "").lower().strip() == "chunked":
        out = bytearray()
        while True:
            size_line = handler.rfile.readline().strip()
            if not size_line:
                continue
            size = int(size_line.split(b";", 1)[0], 16)
            if size == 0:
                handler.rfile.readline()  # trailing CRLF
                break
            out += handler.rfile.read(size)
            handler.rfile.readline()  # CRLF after the chunk
        return bytes(out)
    return b""


def is_subagent_request(body):
    """Whether the request body's system prompt names a general-purpose subagent."""
    try:
        messages = json.loads(body).get("messages") or []
    except Exception:
        return False
    for message in messages:
        if message.get("role") != "system":
            continue
        content = message.get("content")
        if isinstance(content, list):
            content = " ".join(
                part.get("text", "") for part in content if isinstance(part, dict)
            )
        if isinstance(content, str) and SUBAGENT_SYSTEM_MARK in content:
            return True
    return False


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_POST(self):
        try:
            body = read_body(self)
        except Exception:
            body = b""

        control = control_value()
        subagent = is_subagent_request(body)
        drop = control == "drop-all" or (control == "drop-subagent" and subagent)
        decision = "drop" if drop else "pass"
        sys.stderr.write(
            "PROXY decision=%s control=%s subagent=%d path=%s\n"
            % (decision, control, 1 if subagent else 0, self.path)
        )
        sys.stderr.flush()

        if drop:
            self.drop_stream()
            return
        self.forward(body)

    def forward(self, body):
        """Forward the request upstream and stream its response back unbuffered."""
        path = self.path if self.path.startswith("/") else "/" + self.path
        target = (UPSTREAM_BASE_PATH + path) or "/"
        headers = {}
        for key, value in self.headers.items():
            if key.lower() in HOP_BY_HOP:
                continue
            headers[key] = value
        headers["Content-Length"] = str(len(body))
        headers["Connection"] = "close"

        conn = self.connect()
        try:
            conn.request("POST", target, body=body, headers=headers)
            resp = conn.getresponse()
            self.send_response(resp.status)
            for key, value in resp.getheaders():
                if key.lower() in HOP_BY_HOP:
                    continue
                self.send_header(key, value)
            self.send_header("Transfer-Encoding", "chunked")
            self.send_header("Connection", "close")
            self.end_headers()
            self.close_connection = True
            reader = getattr(resp, "read1", resp.read)
            while True:
                try:
                    data = reader(65536)
                except Exception:
                    break
                if not data:
                    break
                self.wfile.write(b"%x\r\n%s\r\n" % (len(data), data))
                try:
                    self.wfile.flush()
                except OSError:
                    break
            try:
                self.wfile.write(b"0\r\n\r\n")
                self.wfile.flush()
            except OSError:
                pass
        except Exception as exc:  # upstream unreachable or died mid-stream
            sys.stderr.write("PROXY upstream error: %s\n" % type(exc).__name__)
            sys.stderr.flush()
            if not self.wfile.closed:
                try:
                    self.send_response(502)
                    self.send_header("Content-Length", "0")
                    self.end_headers()
                except Exception:
                    pass
        finally:
            conn.close()

    def drop_stream(self):
        """A few response bytes, then a hard abort: a stream cut mid-flight."""
        self.close_connection = True
        payload = b'data: {"choices":[{"delta":{"content":"x"}}]}\n\n'
        try:
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Transfer-Encoding", "chunked")
            self.end_headers()
            self.wfile.write(b"%x\r\n%s\r\n" % (len(payload), payload))
            self.wfile.flush()
        except OSError:
            pass
        # No terminating chunk: the client sees an unexpected end of the stream.
        try:
            self.connection.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        try:
            self.connection.close()
        except OSError:
            pass

    def connect(self):
        if UPSTREAM_TLS:
            return http.client.HTTPSConnection(UPSTREAM_HOST, UPSTREAM_PORT, timeout=120)
        return http.client.HTTPConnection(UPSTREAM_HOST, UPSTREAM_PORT, timeout=120)

    def log_message(self, *args):
        pass


class Server(http.server.ThreadingHTTPServer):
    allow_reuse_address = True
    daemon_threads = True


if __name__ == "__main__":
    sys.stderr.write("LISTENING %d upstream=%s\n" % (LISTEN_PORT, UPSTREAM))
    sys.stderr.flush()
    Server(("127.0.0.1", LISTEN_PORT), Handler).serve_forever()
