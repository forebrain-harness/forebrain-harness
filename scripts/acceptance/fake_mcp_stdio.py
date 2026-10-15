#!/usr/bin/env python3
"""A minimal stdio MCP server for driving forebrain's project-level MCP path.

Speaks just enough of the MCP protocol over stdin/stdout for the client to
connect, list one tool, and call it: initialize -> initialized -> tools/list ->
tools/call. Used only by local acceptance runs.

Args: <tool-name> [server-name] [mode]
  mode "text" (default) echoes the input as a plain text block.
  mode "image" makes tools/call answer with an MCP image content block (the
    shape resultJSONContainsImage/containsImageBlock recognize). The acceptance
    run uses it to prove an MCP screenshot reaches the model as pixels and
    lands under the primary agent workspace.
  mode "image_mixed" answers with a text block, an image/webp block above the
    per-part budget (no standard-library decoder could downscale it, so the
    part build must fail), and a good 64x64 PNG — proving one unsendable block
    degrades to a line instead of failing the whole call.
  mode "image_oversize" answers with a text block, an image/png block whose
    decoded size exceeds the 48 MiB per-block limit, and a good PNG — proving
    the pre-decode guard refuses the block without touching disk and the good
    block still reaches the model.
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

# The hostile payloads are built lazily (a 48 MiB block is only worth
# materialising when the mode actually answers with it).
_PAYLOADS = {}


def mixed_webp_b64():
    """A RIFF/WEBP container above the 3 MiB per-part budget.

    The bytes need not decode as a real webp: ImagePartWithin refuses an
    over-budget webp before any decode (no standard-library webp decoder
    exists), which is exactly the branch under test.
    """
    if "webp" not in _PAYLOADS:
        body = b"WEBP" + b"\x00" * (4 << 20)
        webp = b"RIFF" + struct.pack("<I", len(body)) + body
        _PAYLOADS["webp"] = base64.b64encode(webp).decode("ascii")
    return _PAYLOADS["webp"]


def oversize_png_b64():
    """Deterministic zero bytes whose decoded size is just over the 48 MiB limit."""
    if "oversize" not in _PAYLOADS:
        _PAYLOADS["oversize"] = base64.b64encode(b"\x00" * ((48 << 20) + 1)).decode("ascii")
    return _PAYLOADS["oversize"]



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
        elif MODE == "image_mixed":
            content = [
                {"type": "text", "text": "mixed shot"},
                {"type": "image", "data": mixed_webp_b64(), "mimeType": "image/webp"},
                {"type": "image", "data": IMAGE_B64, "mimeType": "image/png"},
            ]
        elif MODE == "image_oversize":
            content = [
                {"type": "text", "text": "oversize shot"},
                {"type": "image", "data": oversize_png_b64(), "mimeType": "image/png"},
                {"type": "image", "data": IMAGE_B64, "mimeType": "image/png"},
            ]
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
