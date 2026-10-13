package llm

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// swapURLTemplate points the release download at an httptest server for the
// duration of a test and returns the restore function.
func swapURLTemplate(template string) func() {
	previous := modelsJSONURLTemplate
	modelsJSONURLTemplate = template
	return func() { modelsJSONURLTemplate = previous }
}

func TestInstallSeedsModelsJSONFromAssetsDir(t *testing.T) {
	src := t.TempDir()
	content := []byte(`{"deepseek/deepseek-chat":{"id":"deepseek/deepseek-chat","name":"DeepSeek Chat","limit":{"context":128000,"output":8192}}}`)
	if err := os.WriteFile(filepath.Join(src, "models.json"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(AssetsDirEnv, src)

	home := t.TempDir()
	if err := Install(home, "(devel)"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(home, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("seeded models.json differs from the source asset")
	}
}

func TestInstallNeverOverwritesExistingModelsJSON(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "models.json"), []byte(`{"a/b":{"id":"a/b","limit":{"context":1}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(AssetsDirEnv, src)

	home := t.TempDir()
	existing := []byte(`{"x":{}}`)
	if err := os.WriteFile(filepath.Join(home, "models.json"), existing, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Install(home, "(devel)"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(home, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, existing) {
		t.Fatalf("existing models.json was overwritten: %s", got)
	}
}

func TestInstallWithoutAssetsOnDevBuildFails(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "the dev build must not download", http.StatusInternalServerError)
	}))
	defer server.Close()
	defer swapURLTemplate(server.URL + "/%s/assets/models.json")()

	t.Setenv(AssetsDirEnv, t.TempDir())

	home := t.TempDir()
	err := Install(home, "(devel)")
	if err == nil {
		t.Fatal("expected an error when the catalog is missing on a dev build")
	}
	if !strings.Contains(err.Error(), "install-assets.sh") {
		t.Fatalf("error %q must name the install script", err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("dev build made %d network requests; want 0", got)
	}
	if _, statErr := os.Stat(filepath.Join(home, "models.json")); !os.IsNotExist(statErr) {
		t.Fatalf("models.json must not be written, stat err=%v", statErr)
	}
}

func TestInstallDownloadsModelsJSONForReleaseVersion(t *testing.T) {
	valid := []byte(`{"openai/gpt-5":{"id":"openai/gpt-5","name":"GPT-5","limit":{"context":400000,"output":128000}}}`)

	t.Run("valid catalog", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1.2.3/assets/models.json" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(valid)
		}))
		defer server.Close()
		defer swapURLTemplate(server.URL + "/%s/assets/models.json")()

		t.Setenv(AssetsDirEnv, t.TempDir())

		home := t.TempDir()
		if err := Install(home, "v1.2.3"); err != nil {
			t.Fatalf("Install: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(home, "models.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, valid) {
			t.Fatalf("downloaded models.json differs from the served payload")
		}
	})

	t.Run("non-json payload", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not json"))
		}))
		defer server.Close()
		defer swapURLTemplate(server.URL + "/%s/assets/models.json")()

		t.Setenv(AssetsDirEnv, t.TempDir())

		home := t.TempDir()
		if err := Install(home, "v1.2.3"); err == nil {
			t.Fatal("expected an error for a non-JSON download")
		}
		if _, statErr := os.Stat(filepath.Join(home, "models.json")); !os.IsNotExist(statErr) {
			t.Fatalf("models.json must not be written, stat err=%v", statErr)
		}
	})
}

func TestInstallAssetsCopiesEntries(t *testing.T) {
	src := t.TempDir()
	content := []byte(`{"a/b":{"id":"a/b","limit":{"context":1}}}`)
	if err := os.WriteFile(filepath.Join(src, "models.json"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	// assetEntries also copies the tokenizers/ directory.
	gz := []byte("not really gzip, just bytes to compare")
	tokenizerDir := filepath.Join(src, "tokenizers", "test-tok")
	if err := os.MkdirAll(tokenizerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tokenizerDir, "tokenizer.json.gz"), gz, 0o644); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	if err := InstallAssets(src, bin); err != nil {
		t.Fatalf("InstallAssets: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(bin, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("installed models.json differs from the source")
	}
	gotGz, err := os.ReadFile(filepath.Join(bin, "tokenizers", "test-tok", "tokenizer.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotGz, gz) {
		t.Fatal("installed tokenizers/ differs from the source")
	}

	if err := InstallAssets(t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("expected an error when the source models.json is missing")
	}
}
