#!/usr/bin/env python3
# make_zh_pdf.py — generate the CJK rendering fixtures for plan 054.
#
# Source, licence and why the PDF fixtures are committed
# ------------------------------------------------------
# Plan 054 teaches read_file to hand a PDF's pages to the model as images.
# The decisive requirement is that CJK text renders as real glyphs, not tofu
# boxes, so the suite needs two PDFs to render and inspect:
#
#   zh-embedded.pdf    — the text uses an *embedded* Noto Sans SC (CFF/OTF).
#                        It renders correctly on any machine because the font
#                        travels inside the file; it is the control.
#   zh-notembedded.pdf — the text references the standard CJK font
#                        STSong-Light and embeds nothing. Like the government
#                        forms and reports this plan targets, rendering it
#                        needs a fallback font; it is the file the non-tofu
#                        test renders and measures.
#
# The embedded font is Noto Sans SC (NotoSansHans-Regular.otf), which this
# repository fetches for the runtime too:
#
#   source:  npm package @embedpdf/fonts-sc@1.0.0 (fonts/NotoSansHans-Regular.otf)
#   upstream: Google Noto Sans CJK <https://github.com/notofonts/noto-cjk>
#   licence: SIL Open Font License 1.1 (see assets/fonts/README.md)
#
# The PDF object layer is hand-built here with the standard library only. The
# embedded fixture also needs to *shrink* the 8.4 MB Noto Sans CJK OTF to just
# the glyphs the line uses, or the committed fixture would carry the whole
# font; that subsetting is done with fontTools (pip install fonttools), which
# is needed only to regenerate the fixtures, never to read them or run the
# tests — the committed PDFs are what every build and test consumes.
#
# The two PDFs are committed under pkg/pdf/testdata/ (the PDFs, not the font)
# because rendering is a property of the bytes PDFium reads — there is no way
# to synthesise a *non-embedded* CJK PDF and then assert the fallback path
# without a real file, and PDFium is the only thing that can produce the
# pixels the non-tofu test measures. They are the repository's only committed
# binary fixtures; the reason is exactly this one.
#
# Usage:
#   scripts/acceptance/make_zh_pdf.py                 # both, into pkg/pdf/testdata
#   scripts/acceptance/make_zh_pdf.py --embed         # only zh-embedded.pdf
#   scripts/acceptance/make_zh_pdf.py --no-embed      # only zh-notembedded.pdf
#   scripts/acceptance/make_zh_pdf.py --out DIR       # write somewhere else
#   scripts/acceptance/make_zh_pdf.py --font PATH     # override the embedded OTF
#   scripts/acceptance/make_zh_pdf.py --pages 9 --out DIR --no-embed
#                                                  # numbered multi-page PDF
#
# Exit status is non-zero when the embedded font is missing (it is fetched by
# scripts/fetch-pdfium-fonts.sh), so a checkout that forgot the fetch fails
# loudly instead of silently writing a degenerate fixture.

import argparse
import os
import struct
import sys

# The line on both fixtures' single page. The ten 甲乙丙…癸 ideographs after the
# fullwidth colon are what the non-tofu test renders, slices and measures.
TEXT = "中文渲染测试：甲乙丙丁戊己庚辛壬癸"
TEXT_EM = 24  # font size in points — both fixtures use the same size and line
PAGE_W, PAGE_H = 595.0, 842.0  # A4 in points
TEXT_X, TEXT_Y = 72.0, 700.0  # the whole 17-glyph line fits inside 595-72 points


# --------------------------------------------------------------------------
# A tiny PDF writer. Objects are collected with their numbers and serialised
# once; the xref table is built from the finished byte offsets.
# --------------------------------------------------------------------------
class PDF:
    def __init__(self):
        self.objs = {}  # number -> bytes (object body, without "N 0 obj")
        self.next = 1

    def reserve(self):
        n = self.next
        self.next += 1
        return n

    def put(self, n, body):
        self.objs[n] = body

    def add(self, body):
        n = self.reserve()
        self.put(n, body)
        return n

    def build(self, root):
        out = bytearray(b"%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
        offsets = {0: 0}
        for n in sorted(self.objs):
            offsets[n] = len(out)
            out += b"%d 0 obj\n" % n + self.objs[n] + b"\nendobj\n"
        xref_at = len(out)
        count = max(self.objs) + 1
        out += b"xref\n0 %d\n" % count
        out += b"0000000000 65535 f \n"
        for n in range(1, count):
            out += b"%010d 00000 n \n" % offsets.get(n, 0)
        out += b"trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n" % (
            count,
            root,
            xref_at,
        )
        return bytes(out)


def stream(dict_body, data):
    return (
        b"<< "
        + dict_body
        + b" /Length %d >>\nstream\n" % len(data)
        + data
        + b"\nendstream"
    )


def escape_name(s):
    return s.encode("latin1")


# --------------------------------------------------------------------------
# TrueType/OpenType table access (enough for the embedded CFF fixture).
# --------------------------------------------------------------------------
def _tables(data):
    n = struct.unpack(">H", data[4:6])[0]
    out = {}
    for i in range(n):
        off = 12 + 16 * i
        tag = data[off : off + 4].decode("latin1")
        offset, length = struct.unpack(">II", data[off + 8 : off + 16])
        out[tag] = (offset, length)
    return out


def _cff_index(data, pos):
    count = struct.unpack(">H", data[pos : pos + 2])[0]
    pos += 2
    if count == 0:
        return [], pos
    off_size = data[pos]
    pos += 1
    offs = [
        int.from_bytes(data[pos + i * off_size : pos + (i + 1) * off_size], "big")
        for i in range(count + 1)
    ]
    base = pos + (count + 1) * off_size
    items = [data[base + offs[i] - 1 : base + offs[i + 1] - 1] for i in range(count)]
    return items, base + offs[-1] - 1


def _cff_top_dict(cff):
    _, pos = _cff_index(cff, 4)  # header, then Name INDEX
    tops, _ = _cff_index(cff, pos)
    top = tops[0]

    def parse(b):
        out, i = {}, 0
        operands = []
        while i < len(b):
            b0 = b[i]
            if b0 <= 21:
                op = b0
                i += 1
                if b0 == 12:
                    op = 1200 + b[i]
                    i += 1
                out[op] = operands
                operands = []
            elif b0 == 28:
                operands.append(struct.unpack(">h", b[i + 1 : i + 3])[0])
                i += 3
            elif b0 == 29:
                operands.append(struct.unpack(">i", b[i + 1 : i + 5])[0])
                i += 5
            elif b0 == 30:
                j = i + 1
                while b[j] != 0x0F:
                    j += 1
                operands.append(0)
                i = j + 1
            elif 32 <= b0 <= 246:
                operands.append(b0 - 139)
                i += 1
            elif 247 <= b0 <= 250:
                operands.append((b0 - 247) * 256 + b[i + 1] + 108)
                i += 2
            elif 251 <= b0 <= 254:
                operands.append(-(b0 - 251) * 256 - b[i + 1] - 108)
                i += 2
            else:
                i += 1
        return out

    return parse(top)


def _cff_charset_gid_to_cid(cff, charset_off, num_glyphs):
    """Return a list where index gid (>=1) holds the CID for that glyph."""
    fmt = cff[charset_off]
    p = charset_off + 1
    cids = []
    if fmt == 0:
        for _ in range(num_glyphs - 1):
            cids.append(struct.unpack(">H", cff[p : p + 2])[0])
            p += 2
    elif fmt in (1, 2):
        while len(cids) < num_glyphs - 1:
            first = struct.unpack(">H", cff[p : p + 2])[0]
            p += 2
            if fmt == 1:
                n_left = cff[p]
                p += 1
            else:
                n_left = struct.unpack(">H", cff[p : p + 2])[0]
                p += 2
            cids.extend(first + k for k in range(n_left + 1))
    else:
        raise SystemExit("unexpected CFF charset format %d" % fmt)
    return cids


def _cmap_unicode_to_gid(data, tables):
    off = tables["cmap"][0]
    n = struct.unpack(">H", data[off + 2 : off + 4])[0]
    sub = None
    for i in range(n):
        pid, eid, soff = struct.unpack(">HHI", data[off + 4 + 8 * i : off + 4 + 8 * i + 8])
        if (pid, eid) in ((3, 1), (0, 3)):
            sub = off + soff
            break
    if sub is None:
        raise SystemExit("no usable cmap subtable")
    fmt = struct.unpack(">H", data[sub : sub + 2])[0]
    if fmt != 4:
        raise SystemExit("only cmap format 4 is supported; got %d" % fmt)
    seg_x2 = struct.unpack(">H", data[sub + 6 : sub + 8])[0]
    seg = seg_x2 // 2
    end = [struct.unpack(">H", data[sub + 14 + 2 * i : sub + 16 + 2 * i])[0] for i in range(seg)]
    start = [
        struct.unpack(">H", data[sub + 16 + seg_x2 + 2 * i : sub + 18 + seg_x2 + 2 * i])[0]
        for i in range(seg)
    ]
    delta = [
        struct.unpack(">h", data[sub + 16 + 2 * seg_x2 + 2 * i : sub + 18 + 2 * seg_x2 + 2 * i])[0]
        for i in range(seg)
    ]
    ro_base = sub + 16 + 3 * seg_x2
    range_off = [
        struct.unpack(">H", data[ro_base + 2 * i : ro_base + 2 * i + 2])[0] for i in range(seg)
    ]

    def gid(cp):
        for i in range(seg):
            if start[i] <= cp <= end[i]:
                if range_off[i] == 0:
                    return (cp + delta[i]) & 0xFFFF
                addr = ro_base + 2 * i + range_off[i] + 2 * (cp - start[i])
                g = struct.unpack(">H", data[addr : addr + 2])[0]
                return 0 if g == 0 else (g + delta[i]) & 0xFFFF
        return 0

    return gid


class Font:
    """The subset of an OTTO/CFF font this generator needs."""

    def __init__(self, data):
        t = _tables(data)
        if data[:4] != b"OTTO":
            raise SystemExit("expected a CFF (OTTO) font; the fallback embed path supports CFF only")
        self.data = data
        head = t["head"][0]
        self.units_per_em = struct.unpack(">H", data[head + 18 : head + 20])[0] or 1000
        self.x_min, self.y_min, self.x_max, self.y_max = struct.unpack(
            ">hhhh", data[head + 36 : head + 44]
        )
        self.postscript_name = self._postscript_name(data, t)
        self.num_glyphs = struct.unpack(">H", data[t["maxp"][0] + 4 : t["maxp"][0] + 6])[0]
        self.advance = self._advances(data, t)
        self.cff = data[t["CFF "][0] : t["CFF "][0] + t["CFF "][1]]
        self._gid_to_cid = _cff_charset_gid_to_cid(
            self.cff, _cff_top_dict(self.cff)[15][0], self.num_glyphs
        )
        self._u2g = _cmap_unicode_to_gid(data, t)

    @staticmethod
    def _postscript_name(data, t):
        off = t["name"][0]
        count, string_off = struct.unpack(">HH", data[off + 2 : off + 6])
        for i in range(count):
            rec = off + 6 + 12 * i
            pid, eid, _lid, nid, ln, soff = struct.unpack(">HHHHHH", data[rec : rec + 12])
            if nid == 6:
                raw = data[off + string_off + soff : off + string_off + soff + ln]
                return raw.decode("utf-16-be") if pid == 3 else raw.decode("latin1")
        raise SystemExit("font has no PostScript name (nameID 6)")

    @staticmethod
    def _advances(data, t):
        hhea = t["hhea"][0]
        num_hmetrics = struct.unpack(">H", data[hhea + 34 : hhea + 36])[0]
        hm = t["hmtx"][0]
        out = []
        last = 0
        for i in range(num_hmetrics):
            last = struct.unpack(">H", data[hm + 4 * i : hm + 4 * i + 2])[0]
            out.append(last)
        return out

    def gid(self, ch):
        return self._u2g(ord(ch))

    def cid(self, ch):
        g = self.gid(ch)
        return self._gid_to_cid[g - 1] if g >= 1 else 0

    def width(self, ch):
        g = self.gid(ch)
        if g == 0:
            return self.advance[0] if self.advance else 1000
        return self.advance[g] if g < len(self.advance) else self.advance[-1]

    def scaled(self, thousand_units):
        return round(thousand_units * 1000.0 / self.units_per_em)


# --------------------------------------------------------------------------
# The two fixtures.
# --------------------------------------------------------------------------
def subset_font(data, text):
    """Shrink an OTF to the glyphs `text` needs, keeping CFF outlines.

    The embedded fixture only uses ~17 glyphs; embedding the full 8.4 MB CJK
    font would make the committed PDF carry the whole thing. fontTools rewrites
    the CFF/cmap/hmtx down to those glyphs so the fixture is a few kilobytes.
    """
    try:
        from fontTools import subset as ftsubset
    except ImportError:
        sys.exit(
            "fontTools is required to regenerate the embedded fixture\n"
            "install it with: pip install fonttools"
        )
    opts = ftsubset.Options()
    opts.set(
        layout_features=[],
        glyph_names=False,
        notdef_outline=True,
        recalc_bounds=True,
        hinting=False,
        drop_tables=["VORG", "vhea", "vmtx", "BASE", "DSIG"],
    )
    import io

    font = ftsubset.load_font(io.BytesIO(data), opts)
    subsetter = ftsubset.Subsetter(options=opts)
    subsetter.populate(text=text)
    subsetter.subset(font)
    buf = io.BytesIO()
    font.save(buf)
    return buf.getvalue()


def fixture_text(page_count):
    text = TEXT
    if page_count > 1:
        text += "".join(footer(number, page_count) for number in range(1, page_count + 1))
    return text


def footer(number, page_count):
    return "第 %d 页 / 共 %d 页" % (number, page_count)


def build_embedded(font, page_count=1):
    pdf = PDF()
    catalog = pdf.reserve()

    # Glyph ids and their widths for the line's characters.
    text = "".join(dict.fromkeys(fixture_text(page_count)))
    cids = [font.cid(c) for c in text]
    widths = {}
    for c in text:
        widths[font.cid(c)] = font.scaled(font.width(c))

    w_array = " ".join("%d [%d]" % (cid, widths[cid]) for cid in sorted(widths))
    # Embed the bare CFF table as a CIDFontType0C font program. A CID-keyed
    # CFF's own charset maps the CIDs written in the content stream to glyphs,
    # which is exactly the Identity-H contract; embedding the whole sfnt as
    # /OpenType instead makes the reader index glyphs by GID and drop the
    # charset, so the same CIDs would address the wrong glyphs.
    fontfile = pdf.add(
        stream(
            b"/Subtype /CIDFontType0C /Length1 %d" % len(font.cff),
            font.cff,
        )
    )
    descriptor = pdf.add(
        (
            b"<< /Type /FontDescriptor /FontName /%s /Flags 4"
            b" /FontBBox [%d %d %d %d] /ItalicAngle 0"
            b" /Ascent %d /Descent %d /CapHeight %d /StemV 80 /FontFile3 %d 0 R >>"
        )
        % (
            escape_name(font.postscript_name),
            font.scaled(font.x_min),
            font.scaled(font.y_min),
            font.scaled(font.x_max),
            font.scaled(font.y_max),
            font.scaled(880),
            font.scaled(-120),
            font.scaled(700),
            fontfile,
        )
    )
    descendant = pdf.add(
        (
            b"<< /Type /Font /Subtype /CIDFontType0 /BaseFont /%s"
            b" /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >>"
            b" /FontDescriptor %d 0 R /DW 1000 /W [%s] >>"
        )
        % (escape_name(font.postscript_name), descriptor, w_array.encode("ascii"))
    )
    tounicode = pdf.add(stream(b"", _tounicode_cmap(cids, text)))
    fontobj = pdf.add(
        (
            b"<< /Type /Font /Subtype /Type0 /BaseFont /%s"
            b" /Encoding /Identity-H /DescendantFonts [%d 0 R] /ToUnicode %d 0 R >>"
        )
        % (escape_name(font.postscript_name), descendant, tounicode)
    )

    pages = _pages(
        pdf, fontobj, page_count, lambda line: "".join("%04X" % font.cid(c) for c in line)
    )
    pdf.put(catalog, b"<< /Type /Catalog /Pages %d 0 R >>" % pages)
    return pdf.build(catalog)


def build_not_embedded(page_count=1):
    pdf = PDF()
    catalog = pdf.reserve()

    # UniGB-UCS2-H maps a UCS-2 code straight to an Adobe-GB1 CID; the font is
    # named but not embedded, so rendering needs a fallback family.
    text = "".join(dict.fromkeys(fixture_text(page_count)))
    tounicode = pdf.add(stream(b"", _tounicode_cmap([ord(c) for c in text], text)))
    descendant = pdf.add(
        (
            b"<< /Type /Font /Subtype /CIDFontType0 /BaseFont /STSong-Light"
            b" /CIDSystemInfo << /Registry (Adobe) /Ordering (GB1) /Supplement 2 >>"
            b" /FontDescriptor << /Type /FontDescriptor /FontName /STSong-Light"
            b" /Flags 4 /FontBBox [-25 -254 1000 880] /ItalicAngle 0"
            b" /Ascent 880 /Descent -120 /CapHeight 700 /StemV 80 >> /DW 1000 >>"
        )
    )
    fontobj = pdf.add(
        (
            b"<< /Type /Font /Subtype /Type0 /BaseFont /STSong-Light"
            b" /Encoding /UniGB-UCS2-H /DescendantFonts [%d 0 R] /ToUnicode %d 0 R >>"
        )
        % (descendant, tounicode)
    )
    pages = _pages(
        pdf, fontobj, page_count, lambda line: "".join("%04X" % ord(c) for c in line)
    )
    pdf.put(catalog, b"<< /Type /Catalog /Pages %d 0 R >>" % pages)
    return pdf.build(catalog)


def _tounicode_cmap(codes, text):
    rows = []
    for code, ch in zip(codes, text):
        rows.append("<%04X> <%04X>" % (code, ord(ch)))
    body = (
        "/CIDInit /ProcSet findresource begin\n"
        "12 dict begin\nbegincmap\n"
        "/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n"
        "/CMapName /Adobe-Identity-UCS def\n/CMapType 2 def\n"
        "1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n"
    )
    body += "%d beginbfchar\n%s\nendbfchar\n" % (len(rows), "\n".join(rows))
    body += "endcmap\nCMapName currentdict /CMap defineresource pop\nend\nend\n"
    return body.encode("ascii")


def _pages(pdf, fontobj, page_count, encode):
    pages = pdf.reserve()
    kids = []
    for number in range(1, page_count + 1):
        content = "BT /F1 %d Tf %g %g Td <%s> Tj ET" % (
            TEXT_EM, TEXT_X, TEXT_Y, encode(TEXT)
        )
        if page_count > 1:
            content += "\nBT /F1 18 Tf 72 72 Td <%s> Tj ET" % encode(footer(number, page_count))
        contents = pdf.add(stream(b"", content.encode("ascii")))
        page = pdf.add(
            (
                b"<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %g %g]"
                b" /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>"
            )
            % (pages, PAGE_W, PAGE_H, fontobj, contents)
        )
        kids.append(b"%d 0 R" % page)
    pdf.put(
        pages, b"<< /Type /Pages /Kids [%s] /Count %d >>" % (b" ".join(kids), page_count)
    )
    return pages


def default_font(repo_root):
    return os.path.join(repo_root, "assets", "fonts", "NotoSansSC-Regular.otf")


def main(argv):
    repo_root = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
    ap = argparse.ArgumentParser(description="generate the plan-054 CJK PDF fixtures")
    ap.add_argument("--out", default=os.path.join(repo_root, "pkg", "pdf", "testdata"))
    ap.add_argument("--font", default=default_font(repo_root))
    ap.add_argument("--embed", action="store_true", help="write only zh-embedded.pdf")
    ap.add_argument("--no-embed", action="store_true", help="write only zh-notembedded.pdf")
    ap.add_argument("--pages", type=int, default=1, help="number of pages (default: 1)")
    args = ap.parse_args(argv)
    if args.pages < 1:
        ap.error("--pages must be at least 1")

    os.makedirs(args.out, exist_ok=True)
    want_embed = args.embed or not args.no_embed
    want_no_embed = args.no_embed or not args.embed

    if want_embed:
        if not os.path.exists(args.font):
            sys.exit(
                "embedded font not found: %s\nrun scripts/fetch-pdfium-fonts.sh first" % args.font
            )
        with open(args.font, "rb") as fh:
            font = Font(subset_font(fh.read(), fixture_text(args.pages)))
        path = os.path.join(args.out, "zh-embedded.pdf")
        with open(path, "wb") as fh:
            fh.write(build_embedded(font, args.pages))
        print("wrote %s (%d bytes)" % (path, os.path.getsize(path)))

    if want_no_embed:
        path = os.path.join(args.out, "zh-notembedded.pdf")
        with open(path, "wb") as fh:
            fh.write(build_not_embedded(args.pages))
        print("wrote %s (%d bytes)" % (path, os.path.getsize(path)))


if __name__ == "__main__":
    main(sys.argv[1:])
