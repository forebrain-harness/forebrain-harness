# `assets/fonts` — CJK fallback fonts for PDF rendering

These fonts are what `pkg/pdf` hands to PDFium's font loader so a PDF that
references a standard CJK font *without embedding one* (STSong-Light, SimSun,
AdobeSongStd — the norm in Chinese forms and reports) renders real glyphs
instead of tofu boxes. GB2312/Simplified text falls back to Noto Sans SC; Big5/
Traditional text to Noto Sans TC.

`scripts/fetch-pdfium-fonts.sh` downloads them; the fonts themselves are **not
committed** (the same rule as `third_party/libtokenizers/`) — only the script,
the pinned versions and the sha256s are. `pkg/process` seeds them into
`$FOREBRAIN_HOME/fonts` at startup, and packaging (`make build`, the npm
packages, the container image) carries them beside the binary.

| File | Font | Source | Version | Licence | SHA256 |
|------|------|--------|---------|---------|--------|
| `NotoSansSC-Regular.otf` | Noto Sans SC (NotoSansHans-Regular) | npm `@embedpdf/fonts-sc` | 1.0.0 | SIL OFL 1.1 | `7db3c634bbd0301b3082a80ffcac2f422e8ef9ce0730f8b279c3e8f756598f68` |
| `NotoSansTC-Regular.otf` | Noto Sans TC (NotoSansHant-Regular) | npm `@embedpdf/fonts-tc` | 1.0.0 | SIL OFL 1.1 | `81f39cffba8b45367a21249720782603b852bee71aad710f8f66fec3d3776af3` |

## Provenance

The upstream fonts are Google's **Noto Sans CJK**
(<https://github.com/notofonts/noto-cjk>), which Google distributes under the
**SIL Open Font License 1.1**. They are published as Git-LFS blobs and are not
reachable as raw files from this build environment, so this repository takes the
same upstream OTFs verbatim from the EmbedPDF packages
(<https://github.com/embedpdf/embed-pdf-viewer>), whose `package.json` declares
`"license": "OFL-1.1"` and which bundle the noto-cjk files unchanged:

- `@embedpdf/fonts-sc@1.0.0` → `fonts/NotoSansHans-Regular.otf`
- `@embedpdf/fonts-tc@1.0.0` → `fonts/NotoSansHant-Regular.otf`

Both names (`NotoSansHans`/`NotoSansHant`) are the upstream Noto Sans CJK SC/TC
Regular faces. OFL-1.1 permits redistribution and bundling; the fonts are used
here unmodified.

To refresh them:

```sh
scripts/fetch-pdfium-fonts.sh
```
