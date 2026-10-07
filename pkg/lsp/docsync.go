package lsp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"
)

// MaxDocumentBytes: larger files are never given to a server (spec §7.6).
const MaxDocumentBytes = 4 << 20

var (
	ErrFileTooLarge = errors.New("file is larger than 4 MiB")
	ErrNotText      = errors.New("file is not UTF-8 text")
)

// DocSync keeps one instance's open documents in step with the disk. Every
// notification goes out while d.mu is held, so the messages of one document
// reach the server in the order this layer produced them.
type DocSync struct {
	inst        *Instance
	languageFor func(absPath string) string
	maxOpen     int

	mu   sync.Mutex
	docs map[string]*openDoc // clean absolute path → document
}

// openDoc is one document the server has open. mtime is zero when the last
// content the server saw did not come from the disk, which makes the next
// EnsureSynced re-read and reconcile instead of trusting the cache.
type openDoc struct {
	version  int32
	sum      [32]byte
	mtime    time.Time
	size     int64
	language string
	content  []byte
	lastUsed time.Time
}

// NewDocSync binds to one instance. languageFor maps an absolute path to its
// languageId; maxOpen <= 0 means 64.
func NewDocSync(inst *Instance, languageFor func(absPath string) string, maxOpen int) *DocSync {
	if maxOpen <= 0 {
		maxOpen = 64
	}
	return &DocSync{
		inst:        inst,
		languageFor: languageFor,
		maxOpen:     maxOpen,
		docs:        map[string]*openDoc{},
	}
}

// checkText validates content a server would receive (spec §7.6): at most
// MaxDocumentBytes, no NUL byte, valid UTF-8.
func checkText(content []byte) error {
	if len(content) > MaxDocumentBytes {
		return ErrFileTooLarge
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return ErrNotText
	}
	if !utf8.Valid(content) {
		return ErrNotText
	}
	return nil
}

// EnsureSynced opens absPath from disk, or sends didChange if the disk
// differs from what the server has. Returns the synced version and content.
func (d *DocSync) EnsureSynced(ctx context.Context, absPath string) (int32, []byte, error) {
	path := filepath.Clean(absPath)
	info, err := os.Stat(path)
	if err != nil {
		return 0, nil, err
	}
	if info.Size() > MaxDocumentBytes {
		return 0, nil, ErrFileTooLarge
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, nil, err
	}
	if err := checkText(content); err != nil {
		return 0, nil, err
	}
	sum := sha256.Sum256(content)

	d.mu.Lock()
	defer d.mu.Unlock()
	doc, open := d.docs[path]
	switch {
	case open && doc.mtime.Equal(info.ModTime()) && doc.size == info.Size():
		doc.lastUsed = time.Now()
		return doc.version, doc.content, nil
	case open && doc.sum == sum:
		doc.mtime, doc.size, doc.lastUsed = info.ModTime(), info.Size(), time.Now()
		return doc.version, doc.content, nil
	case open:
		version := doc.version + 1
		if err := d.inst.Notify("textDocument/didChange", didChangeParams{
			TextDocument:   VersionedTextDocumentIdentifier{URI: PathToURI(path), Version: version},
			ContentChanges: []textChange{{Text: string(content)}},
		}); err != nil {
			return 0, nil, err
		}
		doc.version, doc.sum = version, sum
		doc.mtime, doc.size, doc.lastUsed = info.ModTime(), info.Size(), time.Now()
		doc.content = content
		return doc.version, doc.content, nil
	default:
		if err := d.evictLocked(); err != nil {
			return 0, nil, err
		}
		language := ""
		if d.languageFor != nil {
			language = d.languageFor(path)
		}
		if err := d.inst.Notify("textDocument/didOpen", didOpenParams{
			TextDocument: TextDocumentItem{URI: PathToURI(path), LanguageID: language, Version: 1, Text: string(content)},
		}); err != nil {
			return 0, nil, err
		}
		now := time.Now()
		d.docs[path] = &openDoc{
			version: 1, sum: sum, mtime: info.ModTime(), size: info.Size(),
			language: language, content: content, lastUsed: now,
		}
		return 1, content, nil
	}
}

// OpenWith opens absPath with the given content (a baseline before an edit);
// if already open it sends didChange with that content instead.
func (d *DocSync) OpenWith(ctx context.Context, absPath string, content []byte) (int32, error) {
	if err := checkText(content); err != nil {
		return 0, err
	}
	return d.put(ctx, filepath.Clean(absPath), content)
}

// Change replaces the whole document (opening it first if needed).
func (d *DocSync) Change(ctx context.Context, absPath string, content []byte) (int32, error) {
	if err := checkText(content); err != nil {
		return 0, err
	}
	return d.put(ctx, filepath.Clean(absPath), content)
}

// put is OpenWith and Change: the content comes from the caller, not the
// disk, so the stored mtime stays zero and the next EnsureSynced reconciles.
func (d *DocSync) put(ctx context.Context, path string, content []byte) (int32, error) {
	sum := sha256.Sum256(content)
	d.mu.Lock()
	defer d.mu.Unlock()
	doc, open := d.docs[path]
	if open {
		if doc.sum == sum {
			doc.lastUsed = time.Now()
			return doc.version, nil
		}
		version := doc.version + 1
		if err := d.inst.Notify("textDocument/didChange", didChangeParams{
			TextDocument:   VersionedTextDocumentIdentifier{URI: PathToURI(path), Version: version},
			ContentChanges: []textChange{{Text: string(content)}},
		}); err != nil {
			return 0, err
		}
		doc.version, doc.sum, doc.content = version, sum, content
		doc.mtime, doc.size, doc.lastUsed = time.Time{}, int64(len(content)), time.Now()
		return version, nil
	}
	if err := d.evictLocked(); err != nil {
		return 0, err
	}
	language := ""
	if d.languageFor != nil {
		language = d.languageFor(path)
	}
	if err := d.inst.Notify("textDocument/didOpen", didOpenParams{
		TextDocument: TextDocumentItem{URI: PathToURI(path), LanguageID: language, Version: 1, Text: string(content)},
	}); err != nil {
		return 0, err
	}
	d.docs[path] = &openDoc{
		version: 1, sum: sum, size: int64(len(content)),
		language: language, content: content, lastUsed: time.Now(),
	}
	return 1, nil
}

// evictLocked closes the least recently used document once the table is
// full, so a new one can open (spec §7.6). The caller holds d.mu.
func (d *DocSync) evictLocked() error {
	if len(d.docs) < d.maxOpen {
		return nil
	}
	var oldestPath string
	var oldest *openDoc
	for path, doc := range d.docs {
		if oldest == nil || doc.lastUsed.Before(oldest.lastUsed) {
			oldestPath, oldest = path, doc
		}
	}
	if oldest == nil {
		return nil
	}
	if err := d.inst.Notify("textDocument/didClose", didCloseParams{
		TextDocument: TextDocumentIdentifier{URI: PathToURI(oldestPath)},
	}); err != nil {
		return err
	}
	delete(d.docs, oldestPath)
	return nil
}

// didOpenParams wraps TextDocumentItem the way textDocument/didOpen sends it.
type didOpenParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

// didChangeParams is a whole-document replacement.
type didChangeParams struct {
	TextDocument   VersionedTextDocumentIdentifier `json:"textDocument"`
	ContentChanges []textChange                    `json:"contentChanges"`
}

type textChange struct {
	Text string `json:"text"`
}

// didSaveParams omits Text when the server did not ask for it.
type didSaveParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Text         string                 `json:"text,omitempty"`
}

type didCloseParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// Save sends didSave (with text when the server asked for it).
func (d *DocSync) Save(ctx context.Context, absPath string) error {
	path := filepath.Clean(absPath)
	d.mu.Lock()
	defer d.mu.Unlock()
	doc, open := d.docs[path]
	if !open {
		return fmt.Errorf("lsp: %s is not open on server %s", path, d.inst.spec.ServerID)
	}
	params := didSaveParams{TextDocument: TextDocumentIdentifier{URI: PathToURI(path)}}
	if d.inst.Capabilities().SaveIncludesText() {
		params.Text = string(doc.content)
	}
	if err := d.inst.Notify("textDocument/didSave", params); err != nil {
		return err
	}
	doc.lastUsed = time.Now()
	return nil
}

// Close sends didClose and forgets the document. A document that is not open
// has nothing to close; Close is idempotent.
func (d *DocSync) Close(ctx context.Context, absPath string) error {
	path := filepath.Clean(absPath)
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, open := d.docs[path]; !open {
		return nil
	}
	if err := d.inst.Notify("textDocument/didClose", didCloseParams{
		TextDocument: TextDocumentIdentifier{URI: PathToURI(path)},
	}); err != nil {
		return err
	}
	delete(d.docs, path)
	return nil
}

// IsOpen reports whether the server has absPath open.
func (d *DocSync) IsOpen(absPath string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, open := d.docs[filepath.Clean(absPath)]
	return open
}

// Content returns the content the server currently has for absPath.
func (d *DocSync) Content(absPath string) (content []byte, version int32, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	doc, open := d.docs[filepath.Clean(absPath)]
	if !open {
		return nil, 0, false
	}
	return doc.content, doc.version, true
}

// Count is the number of open documents.
func (d *DocSync) Count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.docs)
}

// Reopen re-sends didOpen for every document from disk; used as the
// instance's OnRestart. Documents that vanished are forgotten.
//
// OnRestart runs before the instance lets new messages through, so Notify
// would refuse; inside the callback the fresh connection belongs to this
// layer alone, and writing to it directly is what puts every didOpen ahead
// of the first post-restart request (spec §7.8).
func (d *DocSync) Reopen(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for path, doc := range d.docs {
		content, info, err := readTextFile(path)
		if err != nil {
			// Vanished or no longer acceptable text: the restarted server
			// cannot open it.
			delete(d.docs, path)
			continue
		}
		item := TextDocumentItem{
			URI: PathToURI(path), LanguageID: doc.language, Version: 1, Text: string(content),
		}
		if err := d.notifyRestarting("textDocument/didOpen", didOpenParams{TextDocument: item}); err != nil {
			delete(d.docs, path)
			continue
		}
		doc.version = 1
		doc.sum = sha256.Sum256(content)
		doc.mtime, doc.size = info.ModTime(), info.Size()
		doc.content, doc.lastUsed = content, time.Now()
	}
}

// notifyRestarting sends one notification on the live connection even though
// the instance is still restarting (open is not set yet). With no connection
// at all it reports the same error Notify would.
func (d *DocSync) notifyRestarting(method string, params any) error {
	d.inst.mu.Lock()
	gen := d.inst.gen
	d.inst.mu.Unlock()
	if gen == nil {
		return fmt.Errorf("language server %s is not running", d.inst.spec.ServerID)
	}
	return gen.conn.Notify(method, params)
}

// readTextFile stats, reads, and text-checks one file for Reopen.
func readTextFile(path string) ([]byte, os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if info.Size() > MaxDocumentBytes {
		return nil, nil, ErrFileTooLarge
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if err := checkText(content); err != nil {
		return nil, nil, err
	}
	return content, info, nil
}
