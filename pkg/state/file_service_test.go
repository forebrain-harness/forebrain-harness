package state

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Every directory this service writes to belongs to one primary agent, so none
// of them may resolve under the shared FOREBRAIN_HOME. Extracted text and scratch
// copies used to land in <home>/state while the originals were already
// isolated, which exposed one agent's document contents to another.
func TestServiceDirsStayInsideAgentWorkspace(t *testing.T) {
	home := filepath.Join("/tmp", "forebrain-home")
	svc := &FileStore{
		Home:          home,
		WorkspaceRoot: filepath.Join(home, "workspaces", "acme"),
		Cfg:           Config{FilesDirRel: "files", FilesTextDirRel: "files-text", TmpDirRel: "tmp"},
	}

	sharedState := filepath.Join(home, "state")
	for name, dir := range map[string]string{
		"filesDir":     svc.filesDir(),
		"filesTextDir": svc.filesTextDir(),
		"tmpDir":       svc.tmpDir(),
	} {
		if strings.HasPrefix(filepath.Clean(dir), sharedState) {
			t.Fatalf("%s = %s leaks into the shared home state tree", name, dir)
		}
		if !strings.HasPrefix(filepath.Clean(dir), filepath.Clean(svc.WorkspaceRoot)) {
			t.Fatalf("%s = %s is outside the agent workspace %s", name, dir, svc.WorkspaceRoot)
		}
	}
}

// Two primary agents sharing a home must not collide on any of these paths.
func TestServiceDirsAreDisjointAcrossAgents(t *testing.T) {
	home := filepath.Join("/tmp", "forebrain-home")
	acme := &FileStore{Home: home, WorkspaceRoot: filepath.Join(home, "workspaces", "acme"), Cfg: Config{FilesDirRel: "files", FilesTextDirRel: "files-text", TmpDirRel: "tmp"}}
	globex := &FileStore{Home: home, WorkspaceRoot: filepath.Join(home, "workspaces", "globex"), Cfg: Config{FilesDirRel: "files", FilesTextDirRel: "files-text", TmpDirRel: "tmp"}}

	for name, pair := range map[string][2]string{
		"filesDir":     {acme.filesDir(), globex.filesDir()},
		"filesTextDir": {acme.filesTextDir(), globex.filesTextDir()},
		"tmpDir":       {acme.tmpDir(), globex.tmpDir()},
	} {
		if pair[0] == pair[1] {
			t.Fatalf("%s is shared between two primary agents: %s", name, pair[0])
		}
	}
}

func TestParseToTextEmptyPath(t *testing.T) {
	_, _, err := ParseToText(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty path")
	}
}

func TestParseToTextWhitespacePath(t *testing.T) {
	_, _, err := ParseToText(context.Background(), "   ")
	if err == nil {
		t.Error("expected error for whitespace path")
	}
}

func TestParseToTextNonExistentFile(t *testing.T) {
	_, parser, err := ParseToText(context.Background(), "/nonexistent/file.txt")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	// Parser should indicate fallback
	if parser != "go_native" {
		t.Errorf("unexpected parser: %q", parser)
	}
}

func TestParseToTextPlainFile(t *testing.T) {
	tmpDir := t.TempDir()
	content := "Hello, world!\nThis is a test file."
	path := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	result, parser, err := ParseToText(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseToText: %v", err)
	}
	if result != content {
		t.Errorf("result = %q; want %q", result, content)
	}
	if parser != "go_native" {
		t.Errorf("unexpected parser: %q", parser)
	}
}

func TestParseToTextWhitespaceContent(t *testing.T) {
	tmpDir := t.TempDir()
	content := "   \n\n  \t  \n"
	path := filepath.Join(tmpDir, "whitespace.txt")
	os.WriteFile(path, []byte(content), 0o644)

	result, _, err := ParseToText(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseToText: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result for whitespace-only file, got %q", result)
	}
}

func TestParseToTextBinaryFile(t *testing.T) {
	tmpDir := t.TempDir()
	// Write some binary data
	binary := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE}
	path := filepath.Join(tmpDir, "binary.bin")
	os.WriteFile(path, binary, 0o644)

	result, parser, err := ParseToText(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseToText: %v", err)
	}
	_ = result
	_ = parser
	// Should not error even for binary files (best-effort)
}

func TestParseToTextLargeFile(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a file with 10KB of content
	content := make([]byte, 10240)
	for i := range content {
		content[i] = 'A'
	}
	path := filepath.Join(tmpDir, "large.txt")
	os.WriteFile(path, content, 0o644)

	result, _, err := ParseToText(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseToText: %v", err)
	}
	if len(result) != 10240 {
		t.Errorf("result length = %d; want 10240", len(result))
	}
}

func TestParseToTextNewlineVariations(t *testing.T) {
	tmpDir := t.TempDir()
	tests := []struct {
		name    string
		content string
	}{
		{"unix", "line1\nline2\nline3"},
		{"windows", "line1\r\nline2\r\nline3"},
		{"mixed", "line1\nline2\r\nline3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(tmpDir, tt.name+".txt")
			os.WriteFile(path, []byte(tt.content), 0o644)
			result, _, err := ParseToText(context.Background(), path)
			if err != nil {
				t.Fatalf("ParseToText: %v", err)
			}
			// Trimmed output should contain all lines
			if len(result) == 0 {
				t.Error("expected non-empty result")
			}
		})
	}
}

func TestParseToTextUnicodeContent(t *testing.T) {
	tmpDir := t.TempDir()
	content := "hello world\n🌊🐋🐠\nΕλληνικά"
	path := filepath.Join(tmpDir, "unicode.txt")
	os.WriteFile(path, []byte(content), 0o644)

	result, _, err := ParseToText(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseToText: %v", err)
	}
	if result != content {
		t.Errorf("result = %q; want %q", result, content)
	}
}

func TestParseToTextSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "target.txt")
	os.WriteFile(target, []byte("symlink target"), 0o644)
	link := filepath.Join(tmpDir, "link.txt")
	os.Symlink(target, link)

	result, _, err := ParseToText(context.Background(), link)
	if err != nil {
		t.Fatalf("ParseToText: %v", err)
	}
	if result != "symlink target" {
		t.Errorf("result = %q; want 'symlink target'", result)
	}
}

func TestParseToTextDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	_, _, err := ParseToText(context.Background(), tmpDir)
	// Reading a directory should fail or return empty
	_ = err
}

func BenchmarkParseToText(b *testing.B) {
	tmpDir := b.TempDir()
	content := "Benchmark content line\n"
	path := filepath.Join(tmpDir, "bench.txt")
	os.WriteFile(path, []byte(content), 0o644)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = ParseToText(ctx, path)
	}
}

func TestExtractFileRefInfos_EmptyJSON(t *testing.T) {
	refs := ExtractFileRefInfos("")
	if refs != nil {
		t.Fatalf("expected nil for empty JSON, got %v", refs)
	}
	refs = ExtractFileRefInfos("[]")
	if refs != nil {
		t.Fatalf("expected nil for empty array, got %v", refs)
	}
}

func TestExtractFileRefInfos_OnlyFileReferences(t *testing.T) {
	partsJSON := `[
		{"type":"text","text":"hello"},
		{"type":"file_reference","file_id":"/tmp/a.png","label":"a.png","mime_type":"image/png"},
		{"type":"file_reference","file_id":"/tmp/b.jpg","label":"b.jpg","mime_type":"image/jpeg"},
		{"type":"attachment_parse","file_id":"c","summary":"parsed"}
	]`
	refs := ExtractFileRefInfos(partsJSON)
	if len(refs) != 2 {
		t.Fatalf("expected 2 file references, got %d: %v", len(refs), refs)
	}
	if refs[0].FileID != "/tmp/a.png" || refs[0].Label != "a.png" || refs[0].MIMEType != "image/png" {
		t.Fatalf("unexpected ref[0]: %+v", refs[0])
	}
	if refs[1].FileID != "/tmp/b.jpg" || refs[1].Label != "b.jpg" || refs[1].MIMEType != "image/jpeg" {
		t.Fatalf("unexpected ref[1]: %+v", refs[1])
	}
}

func TestExtractFileRefInfos_NoFileReferences(t *testing.T) {
	partsJSON := `[{"type":"text","text":"hello"}]`
	refs := ExtractFileRefInfos(partsJSON)
	if len(refs) != 0 {
		t.Fatalf("expected empty, got %v", refs)
	}
}

func TestExtractFileRefInfos_MalformedJSON(t *testing.T) {
	refs := ExtractFileRefInfos("{not json")
	if refs != nil {
		t.Fatalf("expected nil for malformed JSON, got %v", refs)
	}
}

// TestRemoveStoredDeletesLocalOriginalAndParsedText pins the local half of
// removing an upload's stored bytes: both the original under files/ and the
// extracted text under files-text/ go, and a record whose bytes are already
// gone is not an error.
func TestRemoveStoredDeletesLocalOriginalAndParsedText(t *testing.T) {
	ws := t.TempDir()
	svc := &FileStore{
		Home:          ws,
		WorkspaceRoot: ws,
		Cfg:           Config{FilesDirRel: "files", FilesTextDirRel: "files-text", TmpDirRel: "tmp"},
	}
	f := File{
		ID:             "file-1",
		StorageBackend: string(StorageBackendLocal),
		StorageKey:     "ab/file-1.pdf",
		ParsedTextPath: "file-1.txt",
	}
	original := filepath.Join(ws, "files", "ab", "file-1.pdf")
	text := filepath.Join(ws, "state", "files-text", "file-1.txt")
	for _, p := range []string{original, text} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := svc.RemoveStored(context.Background(), f); err != nil {
		t.Fatalf("RemoveStored: %v", err)
	}
	for _, p := range []string{original, text} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s survived RemoveStored", p)
		}
	}
	if err := svc.RemoveStored(context.Background(), f); err != nil {
		t.Fatalf("removing already-gone bytes = %v, want nil", err)
	}
}

// TestRemoveStoredDeletesTheS3Object pins the remote half: an S3-backed
// record deletes its stored object, addressed by the bucket and key the row
// recorded, against the endpoint the configuration points at.
func TestRemoveStoredDeletesTheS3Object(t *testing.T) {
	var mu sync.Mutex
	var deletions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		mu.Lock()
		deletions = append(deletions, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	svc := &FileStore{
		Home:          t.TempDir(),
		WorkspaceRoot: t.TempDir(),
		Cfg: Config{
			FilesDirRel: "files", FilesTextDirRel: "files-text", TmpDirRel: "tmp",
			OSS: OSSConfig{
				Enabled: true, Endpoint: srv.URL, Region: "oss-default",
				Bucket: "uploads", AccessKey: "ak", SecretKey: "sk", ForcePathStyle: true,
			},
		},
	}
	if err := svc.RemoveStored(context.Background(), File{
		ID:             "file-2",
		StorageBackend: string(StorageBackendS3),
		StorageBucket:  "uploads",
		StorageKey:     "ab/file-2.pdf",
	}); err != nil {
		t.Fatalf("RemoveStored: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(deletions) != 1 || deletions[0] != "DELETE /uploads/ab/file-2.pdf" {
		t.Fatalf("deletions = %v, want exactly DELETE /uploads/ab/file-2.pdf", deletions)
	}
}
