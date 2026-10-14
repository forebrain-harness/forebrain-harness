#!/usr/bin/env python3
"""A minimal stdio MCP server for driving forebrain's project-level MCP path.

Speaks just enough of the MCP protocol over stdin/stdout for the client to
connect, list one tool, and call it: initialize -> initialized -> tools/list ->
tools/call. Used only by local acceptance runs.

Args: <tool-name> [server-name] [mode]
  mode "image" makes tools/call answer with an MCP image content block (the
  shape resultJSONContainsImage/containsImageBlock recognize), instead of the
  plain text echo. The acceptance run uses it to prove an MCP screenshot reaches
  the model as pixels and lands under the primary agent workspace.
"""
import base64
import json
import struct
import sys
import zlib

TOOL_NAME = sys.argv[1] if len(sys.argv) > 1 else "echo"
SERVER_NAME = sys.argv[2] if len(sys.argv) > 2 else "fake-project-mcp"
MODE = sys.argv[3] if len(sys.argv) > 3 else "text"

PNG_MAGIC = b"\x89PNG\r\n\x1a\n"


def png_chunk(typ, data):
    """One PNG chunk: length, type, data, CRC over type+data."""
    body = typ + data
    return struct.pack(">I", len(data)) + body + struct.pack(">I", zlib.crc32(body) & 0xffffffff)


def solid_png(width, height, rgb):
    """A valid width x height RGB PNG of one solid colour.

    Generated rather than embedded: a generated file cannot carry a corrupt
    chunk CRC, which is exactly how the previous base64 constant slipped a
    provider-rejected image past a header-only `file`/`sips` check.
    """
    r, g, b = rgb
    row = b"\x00" + bytes((r, g, b)) * width  # filter byte 0, then RGB triples
    raw = row * height
    return (
        PNG_MAGIC
        + png_chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
        + png_chunk(b"IDAT", zlib.compress(raw, 9))
        + png_chunk(b"IEND", b"")
    )


def png_is_valid(data):
    """True when data is a PNG whose every chunk CRC matches its stored value.

    Kept as a real structural check (magic + per-chunk CRC), not the useless
    `assert base64.b64decode(...)` a corrupt constant used to pass.
    """
    if not data.startswith(PNG_MAGIC):
        return False
    i = len(PNG_MAGIC)
    while i + 12 <= len(data):
        length = struct.unpack(">I", data[i:i + 4])[0]
        typ = data[i + 4:i + 8]
        body = data[i + 8:i + 8 + length]
        stored = struct.unpack(">I", data[i + 8 + length:i + 12 + length])[0]
        if stored != (zlib.crc32(typ + body) & 0xffffffff):
            return False
        i += 12 + length
        if typ == b"IEND":
            return i == len(data)
    return False


# The image the fixture returns in image mode: a real 64x64 PNG, generated at
# import so the bytes are valid by construction.
IMAGE_PNG = solid_png(64, 64, (220, 30, 30))
IMAGE_B64 = base64.b64encode(IMAGE_PNG).decode("ascii")
assert png_is_valid(IMAGE_PNG), "fixture image is not a structurally valid PNG"



def send(msg):
    sys.stdout.write(json.dumps(msg) + "\n")
    sys.stdout.flush()


def handle(req):
    method = req.get("method", "")
    req_id = req.get("id")
    if method == "initialize":
        send({
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "protocolVersion": req.get("params", {}).get("protocolVersion", "2025-06-18"),
                "capabilities": {"tools": {}},
                "serverInfo": {"name": SERVER_NAME, "version": "1.0.0"},
            },
        })
    elif method == "tools/list":
        send({
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "tools": [{
                    "name": TOOL_NAME,
                    "description": "Returns an image content block, or echoes its input (fake MCP server).",
                    "inputSchema": {
                        "type": "object",
                        "properties": {"text": {"type": "string"}},
                        "required": ["text"],
                    },
                }]
            },
        })
    elif method == "tools/call":
        args = req.get("params", {}).get("arguments", {})
        text = args.get("text", "")
        if MODE == "image":
            content = [{"type": "image", "data": IMAGE_B64, "mimeType": "image/png"}]
        else:
            content = [{"type": "text", "text": "echo: " + str(text)}]
        send({
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "content": content,
                "isError": False,
            },
        })
    elif method == "ping":
        send({"jsonrpc": "2.0", "id": req_id, "result": {}})
    else:
        # Anything else gets an answer. Silence is not one: a client that probes
        # a method this server does not implement — the stateless
        # server/discover handshake newer clients open with, for one — would
        # wait for a reply that never comes, and a hung handshake reads as "the
        # server is broken" instead of "the server is older". A JSON-RPC error
        # is what makes the client fall back to the initialize handshake.
        send({
            "jsonrpc": "2.0",
            "id": req_id,
            "error": {"code": -32601, "message": "method not found: " + method},
        })


def main():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except json.JSONDecodeError:
            continue
        # Notifications (no id) get no response.
        if "id" not in req:
            continue
        handle(req)


if __name__ == "__main__":
    main()
