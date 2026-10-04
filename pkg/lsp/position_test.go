package lsp

import (
	"errors"
	"runtime"
	"testing"
	"unicode/utf8"
)

func TestToLSPPosition(t *testing.T) {
	cases := []struct {
		name    string
		content string
		line    int
		column  int
		enc     Encoding
		want    Position
		clamped bool
	}{
		{"ascii start", "ab\n", 1, 1, EncodingUTF16, Position{0, 0}, false},
		{"ascii mid", "ab\n", 1, 2, EncodingUTF16, Position{0, 1}, false},
		{"ascii end", "ab\n", 1, 3, EncodingUTF16, Position{0, 2}, false},
		{"ascii past end clamps", "ab\n", 1, 4, EncodingUTF16, Position{0, 2}, true},
		{"second line", "ab\ncd\n", 2, 2, EncodingUTF16, Position{1, 1}, false},
		{"chinese utf-8", "中文\n", 1, 2, EncodingUTF8, Position{0, 3}, false},
		{"chinese utf-16", "中文\n", 1, 2, EncodingUTF16, Position{0, 1}, false},
		{"chinese utf-32", "中文\n", 1, 2, EncodingUTF32, Position{0, 1}, false},
		{"emoji utf-8", "😀x\n", 1, 2, EncodingUTF8, Position{0, 4}, false},
		{"emoji utf-16", "😀x\n", 1, 2, EncodingUTF16, Position{0, 2}, false},
		{"emoji utf-32", "😀x\n", 1, 2, EncodingUTF32, Position{0, 1}, false},
		{"tab counts one code point", "\ta\n", 1, 2, EncodingUTF16, Position{0, 1}, false},
		{"crlf line two", "a\r\nb\r\n", 2, 1, EncodingUTF16, Position{1, 0}, false},
		{"crlf strips the cr", "a\r\nb\r\n", 1, 2, EncodingUTF16, Position{0, 1}, false},
		{"bom is not a column", "\xEF\xBB\xBFabc\n", 1, 2, EncodingUTF8, Position{0, 1}, false},
		{"bom line end", "\xEF\xBB\xBFabc\n", 1, 4, EncodingUTF8, Position{0, 3}, false},
		{"no trailing newline", "ab\ncd", 2, 3, EncodingUTF16, Position{1, 2}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, clamped, err := ToLSPPosition([]byte(tc.content), tc.line, tc.column, tc.enc)
			if err != nil {
				t.Fatalf("ToLSPPosition: %v", err)
			}
			if got != tc.want || clamped != tc.clamped {
				t.Fatalf("got %+v clamped=%v, want %+v clamped=%v", got, clamped, tc.want, tc.clamped)
			}
		})
	}

	outOfRange := []struct {
		name string
		line int
		want int
	}{
		{"past the end", 3, 2},
		{"zero", 0, 2},
	}
	for _, tc := range outOfRange {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ToLSPPosition([]byte("a\nb\n"), tc.line, 1, EncodingUTF16)
			var oor *LineOutOfRangeError
			if !errors.As(err, &oor) {
				t.Fatalf("err = %v, want *LineOutOfRangeError", err)
			}
			if oor.Line != tc.line || oor.Lines != tc.want {
				t.Fatalf("error = %+v, want line %d of %d lines", *oor, tc.line, tc.want)
			}
		})
	}

	// LineCount agrees with the error's Lines: a trailing newline does not
	// start a new line, an empty file has none.
	for content, want := range map[string]int{"": 0, "a": 1, "a\n": 1, "a\nb": 2, "a\nb\n": 2} {
		if got := LineCount([]byte(content)); got != want {
			t.Fatalf("LineCount(%q) = %d, want %d", content, got, want)
		}
	}
	if text, ok := LineText([]byte("ab\ncd\n"), 2); !ok || text != "cd" {
		t.Fatalf("LineText(2) = %q ok=%v, want \"cd\" true", text, ok)
	}
	if _, ok := LineText([]byte("ab\n"), 2); ok {
		t.Fatal("LineText(2) of a one-line file reported ok")
	}
}

func TestFromLSPPositionRoundTrip(t *testing.T) {
	contents := []string{
		"ab\ncd\n",
		"中文test\n😀x\n",
		"\ta\r\nb\r\n",
		"\xEF\xBB\xBFhello\n",
	}
	encodings := []Encoding{EncodingUTF8, EncodingUTF16, EncodingUTF32}
	for _, content := range contents {
		for _, enc := range encodings {
			lines := splitLines([]byte(content))
			for line := 1; line <= len(lines); line++ {
				for column := 1; column <= utf8.RuneCountInString(lines[line-1])+1; column++ {
					pos, clamped, err := ToLSPPosition([]byte(content), line, column, enc)
					if err != nil {
						t.Fatalf("ToLSPPosition(%q, %d, %d, %s): %v", content, line, column, enc, err)
					}
					if clamped {
						t.Fatalf("column %d clamped inside the line", column)
					}
					gotLine, gotColumn := FromLSPPosition([]byte(content), pos, enc)
					if gotLine != line || gotColumn != column {
						t.Fatalf("round trip of %q %d:%d via %s gave %d:%d", content, line, column, enc, gotLine, gotColumn)
					}
				}
			}
		}
	}

	// A position past the end of the content maps to line+1 / character+1
	// unchanged.
	gotLine, gotColumn := FromLSPPosition([]byte("ab\ncd\n"), Position{Line: 2, Character: 5}, EncodingUTF16)
	if gotLine != 3 || gotColumn != 6 {
		t.Fatalf("past-the-end position gave %d:%d, want 3:6", gotLine, gotColumn)
	}
}

func TestAnchorSymbol(t *testing.T) {
	content := "" +
		"package main\n" + // 1
		"\n" + // 2
		"func main() {\n" + // 3
		"\tfmt.Println(\"hi\")\n" + // 4
		"}\n" + // 5
		"foobar\n" + // 6
		"\n" + // 7
		"\n" + // 8
		"\n" + // 9
		"\n" + // 10
		"pkg::name = 1\n" + // 11
		"obj->field = 2\n" + // 12
		"Class#method()\n" + // 13
		"x := 1\n" // 14

	found := []struct {
		name    string
		line    int
		symbol  string
		wantLin int
		wantCol int
	}{
		{"same line", 4, "Println", 4, 6},
		{"trimmed", 4, "  Println\t", 4, 6},
		{"previous line", 5, "Println", 4, 6},
		{"later line within three", 11, "x", 14, 1},
		{"dot takes the last segment", 4, "fmt.Println", 4, 6},
		{"double colon", 11, "pkg::name", 11, 6},
		{"arrow", 12, "obj->field", 12, 6},
		{"hash", 13, "Class#method", 13, 7},
	}
	for _, tc := range found {
		t.Run(tc.name, func(t *testing.T) {
			line, column, err := AnchorSymbol([]byte(content), tc.line, tc.symbol)
			if err != nil {
				t.Fatalf("AnchorSymbol: %v", err)
			}
			if line != tc.wantLin || column != tc.wantCol {
				t.Fatalf("AnchorSymbol(%d, %q) = %d:%d, want %d:%d", tc.line, tc.symbol, line, column, tc.wantLin, tc.wantCol)
			}
		})
	}

	missing := []struct {
		name   string
		line   int
		symbol string
		from   int
		to     int
	}{
		{"substring is not a match", 6, "foo", 3, 9},
		{"not within three lines", 3, "x", 1, 6},
		{"clamped at the top", 2, "zzz", 1, 5},
		{"clamped at the bottom", 12, "zzz", 9, 14},
	}
	for _, tc := range missing {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := AnchorSymbol([]byte(content), tc.line, tc.symbol)
			var nf *SymbolNotFoundError
			if !errors.As(err, &nf) {
				t.Fatalf("err = %v, want *SymbolNotFoundError", err)
			}
			if nf.Line != tc.line || nf.From != tc.from || nf.To != tc.to {
				t.Fatalf("error = %+v, want line %d searched %d-%d", *nf, tc.line, tc.from, tc.to)
			}
		})
	}
}

func TestPathURI(t *testing.T) {
	// Unix rules, exercised explicitly so the test is not tied to this
	// machine's GOOS.
	if got := pathToURI("/home/a b/x#1.go", "linux"); got != "file:///home/a%20b/x%231.go" {
		t.Fatalf("pathToURI = %q", got)
	}
	got, err := uriToPath("file:///home/a%20b/x%231.go", "linux")
	if err != nil || got != "/home/a b/x#1.go" {
		t.Fatalf("uriToPath = %q, %v", got, err)
	}

	// Windows drive paths.
	if got := pathToURI(`C:\Users\me\x.go`, "windows"); got != "file:///C:/Users/me/x.go" {
		t.Fatalf("pathToURI = %q", got)
	}
	if got := pathToURI(`c:\Users\me\x.go`, "windows"); got != "file:///C:/Users/me/x.go" {
		t.Fatalf("pathToURI with a lowercase drive = %q", got)
	}
	got, err = uriToPath("file:///C:/Users/me/x.go", "windows")
	if err != nil || got != `C:\Users\me\x.go` {
		t.Fatalf("uriToPath = %q, %v", got, err)
	}
	// An escaped drive colon.
	got, err = uriToPath("file:///c%3A/Users/me/x.go", "windows")
	if err != nil || got != `C:\Users\me\x.go` {
		t.Fatalf("uriToPath with an escaped colon = %q, %v", got, err)
	}

	// UNC paths.
	if got := pathToURI(`\\host\share\x`, "windows"); got != "file://host/share/x" {
		t.Fatalf("UNC pathToURI = %q", got)
	}
	got, err = uriToPath("file://host/share/x", "windows")
	if err != nil || got != `\\host\share\x` {
		t.Fatalf("UNC uriToPath = %q, %v", got, err)
	}

	// localhost means local.
	got, err = uriToPath("file://localhost/tmp/x", "linux")
	if err != nil || got != "/tmp/x" {
		t.Fatalf("localhost uriToPath = %q, %v", got, err)
	}

	// The public wrappers follow this machine's GOOS (darwin is unix rules).
	if got := PathToURI("/tmp/a b"); got != "file:///tmp/a%20b" {
		t.Fatalf("PathToURI = %q", got)
	}
	got, err = URIToPath("file:///tmp/a%20b")
	if err != nil || got != "/tmp/a b" {
		t.Fatalf("URIToPath = %q, %v", got, err)
	}
	if _, err := URIToPath("http://x"); err == nil {
		t.Fatal("URIToPath accepted a non-file scheme")
	}

	if !SamePath("/tmp/a/../b", "/tmp/b") {
		t.Fatal("SamePath must clean before comparing")
	}
	caseInsensitive := runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	if got := SamePath("/TMP/B", "/tmp/b"); got != caseInsensitive {
		t.Fatalf("SamePath(/TMP/B, /tmp/b) = %v, want %v on %s", got, caseInsensitive, runtime.GOOS)
	}
}

func FuzzToLSPPosition(f *testing.F) {
	f.Add([]byte("ab\ncd\n"), 1, 1, "utf-16")
	f.Add([]byte("\xEF\xBB\xBF中文\n"), 2, 2, "utf-8")
	f.Add([]byte("\xF0\x9F\x98\x80\n"), 1, 2, "utf-32")
	f.Add([]byte("\xFF\xFE bad\n"), 2, 3, "utf-16")
	f.Add([]byte("a\r\nb"), 5, 4, "")
	f.Fuzz(func(t *testing.T, content []byte, line, column int, enc string) {
		encoding := ParseEncoding(enc)
		pos, _, err := ToLSPPosition(content, line, column, encoding)
		if err != nil {
			var oor *LineOutOfRangeError
			if !errors.As(err, &oor) {
				t.Fatalf("unexpected error: %v", err)
			}
			return
		}
		outLine, outColumn := FromLSPPosition(content, pos, encoding)
		if outLine < 1 || outColumn < 1 {
			t.Fatalf("FromLSPPosition(%+v) = %d:%d", pos, outLine, outColumn)
		}
	})
}
