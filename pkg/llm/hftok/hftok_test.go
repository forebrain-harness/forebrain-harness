package hftok

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"unicode/utf8"
)

// deepseekV3 is a real tokenizer.json (DeepSeek V3) shared with pkg/llm.
const deepseekV3 = "../testdata/deepseek_v3_tokenizer.json"

func TestFromFileCountsDeepSeekV3(t *testing.T) {
	tk, err := FromFile(deepseekV3)
	if err != nil {
		t.Fatalf("FromFile: %v", err)
	}
	defer tk.Close()

	text := "Hello, world! 你好，世界！"
	n := tk.Count(text)
	if n <= 0 {
		t.Fatalf("Count(%q) = %d, want > 0", text, n)
	}
	if max := utf8.RuneCountInString(text); n >= max {
		t.Fatalf("Count(%q) = %d, want < %d (rune count)", text, n, max)
	}
	if got := tk.Count(""); got != 0 {
		t.Fatalf("Count(\"\") = %d, want 0", got)
	}
}

func TestFromBytesMatchesFromFile(t *testing.T) {
	data, err := os.ReadFile(deepseekV3)
	if err != nil {
		t.Fatal(err)
	}
	fromFile, err := FromFile(deepseekV3)
	if err != nil {
		t.Fatalf("FromFile: %v", err)
	}
	defer fromFile.Close()
	fromBytes, err := FromBytes(data)
	if err != nil {
		t.Fatalf("FromBytes: %v", err)
	}
	defer fromBytes.Close()

	text := "func main() {\n\tfmt.Println(\"你好，世界\")\n}\n// mixed 中文 and code\n"
	if a, b := fromFile.Count(text), fromBytes.Count(text); a != b {
		t.Fatalf("FromFile.Count = %d, FromBytes.Count = %d; want equal", a, b)
	}
}

func TestCountSurvivesNulBytes(t *testing.T) {
	tk, err := FromFile(deepseekV3)
	if err != nil {
		t.Fatalf("FromFile: %v", err)
	}
	defer tk.Close()

	a := tk.Count("a")
	if got, want := tk.Count("a\x00b"), a+tk.Count("b")+1; got != want {
		t.Fatalf("Count(\"a\\x00b\") = %d, want %d", got, want)
	}
	if got := tk.Count("a\x00b"); got == a {
		t.Fatalf("Count(\"a\\x00b\") = %d equals Count(\"a\") = %d; the NUL truncated the input", got, a)
	}
}

func TestCountIsSafeConcurrently(t *testing.T) {
	tk, err := FromFile(deepseekV3)
	if err != nil {
		t.Fatalf("FromFile: %v", err)
	}
	defer tk.Close()

	texts := []string{"alpha beta gamma", "你好，世界！", "func foo() {}", "1234567890", "the quick brown fox", "mixed 中英 text 123"}
	want := make([]int, len(texts))
	for i, s := range texts {
		want[i] = tk.Count(s)
	}

	const goroutines = 8
	const iterations = 50
	var wg sync.WaitGroup
	failures := make(chan string, goroutines*iterations)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				idx := (g + i) % len(texts)
				if got := tk.Count(texts[idx]); got != want[idx] {
					failures <- fmt.Sprintf("Count(%q) = %d, want %d", texts[idx], got, want[idx])
					return
				}
			}
		}()
	}
	wg.Wait()
	close(failures)
	for msg := range failures {
		t.Error(msg)
	}
}

func TestFromFileRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	tk, err := FromFile(path)
	if err == nil {
		tk.Close()
		t.Fatal("FromFile(garbage) returned no error")
	}
}

func TestNilTokenizerCountsZero(t *testing.T) {
	var tk *Tokenizer
	if got := tk.Count("anything"); got != 0 {
		t.Fatalf("nil.Count = %d, want 0", got)
	}
	if err := tk.Close(); err != nil {
		t.Fatalf("nil.Close = %v, want nil", err)
	}
}
