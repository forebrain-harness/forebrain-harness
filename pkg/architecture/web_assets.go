package architecture

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The web UI ships inside the gateway binary (pkg/gateway/dist) and is served to
// a browser on an intranet that has no public network: an asset the page loads
// from the internet never arrives, and the page renders incomplete or not at
// all. What a shipped build may reference is therefore a deployment contract, one
// this package states so a build that breaks it fails a test rather than a
// customer's first page load.
//
// The patterns below name the places a document loads a resource from — the
// browser fetches them before it draws anything. They deliberately do not match
// every occurrence of "https://": a documentation link written in text is not a
// dependency, because nothing fetches it.
var (
	// <script src>, <link href>, <img src>, <iframe src>, <source src>, ...
	webAssetTagPattern = regexp.MustCompile(`(?is)<(?:script|link|img|iframe|source|video|audio|embed|object)\b[^>]*?\b(?:src|href)\s*=\s*["']([^"']*)["']`)
	// CSS, and inline style attributes: url(...) in any quoting style.
	webAssetURLPattern = regexp.MustCompile(`(?is)url\(\s*["']?([^"')]*)["']?\s*\)`)
	// @import "…" and @import url("…").
	webAssetImportPattern = regexp.MustCompile(`(?is)@import\s+(?:url\()?\s*["']([^"']*)["']`)
)

// WebDocumentExtensions are the files a browser parses for resource references,
// and the only ones ExternalWebAssets reads.
var WebDocumentExtensions = []string{".html", ".css", ".js"}

// WebAssetReference is one resource a shipped document loads from an absolute
// address: the document that names it, relative to the directory scanned, and the
// address as written.
type WebAssetReference struct {
	File   string
	Target string
}

// ExternalWebAssets lists every resource the documents under dir load from an
// absolute address, and reports how many documents it read.
//
// A target counts as external when it is an absolute http(s) URL, or
// protocol-relative ("//host/…") and so resolves to a host of its own. A relative
// path, an anchor, a data: URI and anything else the build carries itself are
// fine: those are served by whatever serves the document.
func ExternalWebAssets(dir string) ([]WebAssetReference, int, error) {
	var (
		references []WebAssetReference
		scanned    int
	)
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !webDocument(path) {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		scanned++
		file, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			file = path
		}
		content := string(data)
		for _, pattern := range []*regexp.Regexp{webAssetTagPattern, webAssetURLPattern, webAssetImportPattern} {
			for _, match := range pattern.FindAllStringSubmatch(content, -1) {
				if !externalWebAsset(match[1]) {
					continue
				}
				references = append(references, WebAssetReference{File: filepath.ToSlash(file), Target: strings.TrimSpace(match[1])})
			}
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	sort.SliceStable(references, func(i, j int) bool {
		if references[i].File != references[j].File {
			return references[i].File < references[j].File
		}
		return references[i].Target < references[j].Target
	})
	return references, scanned, nil
}

// webDocument reports whether path is a file a browser parses for resource
// references.
func webDocument(path string) bool {
	extension := strings.ToLower(filepath.Ext(path))
	for _, wanted := range WebDocumentExtensions {
		if extension == wanted {
			return true
		}
	}
	return false
}

// externalWebAsset reports whether a reference target would be fetched from
// outside the served build.
func externalWebAsset(target string) bool {
	lower := strings.ToLower(strings.TrimSpace(target))
	return strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "//")
}
