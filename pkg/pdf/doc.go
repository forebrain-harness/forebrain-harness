// Package pdf renders PDF pages to PNG so the model can read a PDF by looking
// at its pages, the way it reads an image.
//
// It links PDFium (Apache-2.0) through github.com/klippa-app/go-pdfium; the
// native library is fetched by scripts/fetch-pdfium.sh into
// third_party/pdfium/<goos>_<goarch>/ and found at build time through
// pkg-config. RenderFile renders up to DefaultMaxPages pages starting at the
// 1-based start page, at DefaultDPI, and encodes them as PNG, reporting the
// document's page count alongside so a caller can say which pages it did not
// show.
//
// CJK: a PDF that references a standard CJK font without embedding it (STSong,
// SimSun, AdobeSongStd — common in forms and reports) needs a fallback font,
// or its glyphs render as tofu boxes. FontDirs resolves the directories handed
// to PDFium's font loader; when this repository's own Noto Sans SC/TC files
// are present (seeded into $FOREBRAIN_HOME/fonts by pkg/process) they are used,
// and on Linux the platform font directories are added alongside them because
// PDFium's UserFontPaths replaces, rather than augments, its defaults.
package pdf
