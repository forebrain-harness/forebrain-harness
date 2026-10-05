// The file store: uploads, parsing, S3 backing, references, and config.
package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/google/uuid"
)

type FileStore struct {
	DB   *sql.DB
	Home string
	// WorkspaceRoot is the active primary agent's workspace directory. Uploaded
	// originals are stored under <WorkspaceRoot>/files so they fall inside the
	// agent's allowed roots and can be opened by its own file tools. When empty
	// it defaults to <Home>/workspace.
	WorkspaceRoot string
	Cfg           Config
}

// workspaceRoot resolves the active workspace directory, defaulting to
// <Home>/workspace when WorkspaceRoot is unset.
func (s *FileStore) workspaceRoot() string {
	if ws := strings.TrimSpace(s.WorkspaceRoot); ws != "" {
		return ws
	}
	return filepath.Join(strings.TrimSpace(s.Home), "workspace")
}

func (s *FileStore) filesDir() string {
	return filepath.Join(s.workspaceRoot(), s.Cfg.FilesDirRel)
}

// filesTextDir and tmpDir hold the extracted text and scratch copies of the
// very files filesDir stores, so they hang off the same per-agent workspace
// root. Rooting them at the shared home left one agent's document contents
// readable from another agent's tree while the originals stayed isolated.
func (s *FileStore) filesTextDir() string {
	return filepath.Join(s.workspaceRoot(), "state", s.Cfg.FilesTextDirRel)
}

func (s *FileStore) tmpDir() string {
	return filepath.Join(s.workspaceRoot(), "state", s.Cfg.TmpDirRel)
}

func sniffMediaType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext != "" {
		if mt := mime.TypeByExtension(ext); mt != "" {
			return mt
		}
	}
	return "application/octet-stream"
}

func (s *FileStore) CreateFromReader(ctx context.Context, sessionID, originalName string, r io.Reader, sizeHint int64, mediaType string) (*File, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id required")
	}
	if mediaType == "" {
		mediaType = sniffMediaType(originalName)
	}
	id := uuid.NewString()
	now := time.Now().Unix()

	backend := s.Cfg.DefaultStorageBackend
	if backend != StorageBackendLocal && backend != StorageBackendS3 {
		backend = StorageBackendLocal
	}

	sum := sha256.New()
	var (
		sizeBytes int64
		relpath   string
	)

	if backend == StorageBackendLocal {
		if err := os.MkdirAll(s.filesDir(), 0o755); err != nil {
			return nil, err
		}
		tmp := filepath.Join(s.filesDir(), fmt.Sprintf("%s.uploading", id))
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		n, err := io.Copy(io.MultiWriter(f, sum), r)
		if err != nil {
			_ = os.Remove(tmp)
			return nil, err
		}
		sizeBytes = n
		sha := hex.EncodeToString(sum.Sum(nil))
		prefix := sha[:2]
		ext := strings.ToLower(filepath.Ext(originalName))
		relpath = filepath.Join(prefix, id+ext)
		final := filepath.Join(s.filesDir(), relpath)
		if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
			_ = os.Remove(tmp)
			return nil, err
		}
		if err := os.Rename(tmp, final); err != nil {
			_ = os.Remove(tmp)
			return nil, err
		}
		frec := &File{
			ID:             id,
			SessionID:      sessionID,
			OriginalName:   strings.TrimSpace(originalName),
			MediaType:      strings.TrimSpace(mediaType),
			SizeBytes:      sizeBytes,
			SHA256:         sha,
			StorageBackend: string(StorageBackendLocal),
			StorageBucket:  "",
			StorageKey:     filepath.ToSlash(relpath),
			ParseStatus:    string(ParseStatusPending),
			ParseError:     "",
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := s.insert(ctx, frec); err != nil {
			return nil, err
		}
		return frec, nil
	}

	// S3 backend (Aliyun OSS S3 compatible)
	if !s.Cfg.OSS.Enabled {
		return nil, fmt.Errorf("oss disabled")
	}
	ext := strings.ToLower(filepath.Ext(originalName))
	tmp := filepath.Join(s.tmpDir(), fmt.Sprintf("%s.uploading", id))
	if err := os.MkdirAll(filepath.Dir(tmp), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(io.MultiWriter(f, sum), r)
	_ = f.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	sizeBytes = n
	if sizeHint > 0 && sizeBytes == 0 {
		sizeBytes = sizeHint
	}
	sha := hex.EncodeToString(sum.Sum(nil))
	prefix := sha[:2]
	key := filepath.ToSlash(filepath.Join(prefix, id+ext))
	_, err = func() (string, error) {
		ff, err := os.Open(tmp)
		if err != nil {
			return "", err
		}
		defer ff.Close()
		return s.PutOriginalObject(ctx, key, ff, mediaType)
	}()
	_ = os.Remove(tmp)
	if err != nil {
		return nil, err
	}
	frec := &File{
		ID:             id,
		SessionID:      sessionID,
		OriginalName:   strings.TrimSpace(originalName),
		MediaType:      strings.TrimSpace(mediaType),
		SizeBytes:      sizeBytes,
		SHA256:         sha,
		StorageBackend: string(StorageBackendS3),
		StorageBucket:  strings.TrimSpace(s.Cfg.OSS.Bucket),
		StorageKey:     key,
		ParseStatus:    string(ParseStatusPending),
		ParseError:     "",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.insert(ctx, frec); err != nil {
		return nil, err
	}
	return frec, nil
}

func (s *FileStore) UpdateParsedText(ctx context.Context, fileID string, relpath string) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("nil db")
	}
	fileID = strings.TrimSpace(fileID)
	relpath = strings.TrimSpace(relpath)
	if fileID == "" || relpath == "" {
		return fmt.Errorf("file_id and relpath required")
	}
	now := time.Now().Unix()
	_, err := s.DB.ExecContext(ctx, `
UPDATE fb_files
SET parse_status=?, parsed_text_path=?, updated_at=?, parse_error=''
WHERE id=?`,
		string(ParseStatusDone), filepath.ToSlash(relpath), now, fileID,
	)
	return err
}

func (s *FileStore) UpdateParseFailed(ctx context.Context, fileID string, errMsg string) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("nil db")
	}
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return fmt.Errorf("file_id required")
	}
	now := time.Now().Unix()
	_, err := s.DB.ExecContext(ctx, `
UPDATE fb_files
SET parse_status=?, parse_error=?, updated_at=?
WHERE id=?`,
		string(ParseStatusFailed), strings.TrimSpace(errMsg), now, fileID,
	)
	return err
}

func (s *FileStore) insert(ctx context.Context, f *File) error {
	if f == nil {
		return fmt.Errorf("nil file")
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO fb_files(
  id, session_id, original_name, media_type, size_bytes, sha256,
  storage_backend, storage_bucket, storage_key,
  parse_status, parsed_text_path, parse_error, created_at, updated_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.ID, f.SessionID, f.OriginalName, f.MediaType, f.SizeBytes, f.SHA256,
		f.StorageBackend, f.StorageBucket, f.StorageKey,
		f.ParseStatus, f.ParsedTextPath, f.ParseError, f.CreatedAt, f.UpdatedAt,
	)
	return err
}

// Get returns the file with id that belongs to agentID's conversation. A file
// uploaded by another agent's session is indistinguishable from one that does
// not exist: the lookup joins the owning session and filters on the tenant.
func (s *FileStore) Get(ctx context.Context, agentID, id string) (*File, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("nil db")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("id required")
	}
	var f File
	err := s.DB.QueryRowContext(ctx, `
SELECT f.id, f.session_id, f.original_name, f.media_type, f.size_bytes, f.sha256,
       f.storage_backend, f.storage_bucket, f.storage_key,
       f.parse_status, f.parsed_text_path, f.parse_error, f.created_at, f.updated_at
FROM fb_files f
JOIN fb_sessions s ON s.id=f.session_id AND s.agent_id=?
WHERE f.id=?`, strings.TrimSpace(agentID), id).Scan(
		&f.ID, &f.SessionID, &f.OriginalName, &f.MediaType, &f.SizeBytes, &f.SHA256,
		&f.StorageBackend, &f.StorageBucket, &f.StorageKey,
		&f.ParseStatus, &f.ParsedTextPath, &f.ParseError, &f.CreatedAt, &f.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("not found")
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (s *FileStore) LocalPath(f *File) (string, bool) {
	if f == nil {
		return "", false
	}
	if strings.EqualFold(f.StorageBackend, string(StorageBackendLocal)) && strings.TrimSpace(f.StorageKey) != "" {
		return filepath.Join(s.filesDir(), filepath.FromSlash(f.StorageKey)), true
	}
	return "", false
}

// ParsedTextLocalPath resolves the extracted text of a file. Parsed text is
// always kept as a local file under the agent's workspace; there is no remote
// backend for it, so a recorded path is a path.
func (s *FileStore) ParsedTextLocalPath(f *File) (string, bool) {
	if f == nil {
		return "", false
	}
	if rp := strings.TrimSpace(f.ParsedTextPath); rp != "" {
		return filepath.Join(s.filesTextDir(), filepath.FromSlash(rp)), true
	}
	return "", false
}

// EnsureLocalFile returns an absolute, agent-readable path to the original
// uploaded file. Local-backed files already live under <workspace>/files and
// their path is returned directly. S3-backed files are materialized into
// <workspace>/files/<id>/<name> so they too fall inside the agent's allowed
// roots and can be opened by its file tools. The returned File carries the
// original name and media type for building a reference for the agent.
func (s *FileStore) EnsureLocalFile(ctx context.Context, agentID, fileID string) (absPath string, f *File, err error) {
	if s == nil {
		return "", nil, fmt.Errorf("nil service")
	}
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return "", nil, fmt.Errorf("file_id required")
	}
	rec, err := s.Get(ctx, agentID, fileID)
	if err != nil {
		return "", nil, err
	}
	if p, ok := s.LocalPath(rec); ok {
		return p, rec, nil
	}
	if strings.EqualFold(rec.StorageBackend, string(StorageBackendS3)) && strings.TrimSpace(rec.StorageKey) != "" {
		name := strings.TrimSpace(rec.OriginalName)
		if name == "" {
			name = fileID
		}
		dir := filepath.Join(s.filesDir(), fileID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", nil, err
		}
		dst := filepath.Join(dir, filepath.Base(name))
		// Reuse a prior materialization when present.
		if st, statErr := os.Stat(dst); statErr == nil && st.Size() == rec.SizeBytes {
			return dst, rec, nil
		}
		rc, _, _, _, getErr := s.GetOriginalObject(ctx, rec.StorageBucket, rec.StorageKey)
		if getErr != nil {
			return "", nil, getErr
		}
		raw, readErr := io.ReadAll(rc)
		_ = rc.Close()
		if readErr != nil {
			return "", nil, readErr
		}
		if writeErr := os.WriteFile(dst, raw, 0o600); writeErr != nil {
			return "", nil, writeErr
		}
		return dst, rec, nil
	}
	return "", nil, fmt.Errorf("file not available locally")
}

func (s *FileStore) EnsureParsedText(ctx context.Context, agentID, fileID string) (text string, parser string, err error) {
	if s == nil {
		return "", "", fmt.Errorf("nil service")
	}
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return "", "", fmt.Errorf("file_id required")
	}
	f, err := s.Get(ctx, agentID, fileID)
	if err != nil {
		return "", "", err
	}
	if p, ok := s.ParsedTextLocalPath(f); ok {
		raw, rerr := os.ReadFile(p)
		if rerr == nil {
			return strings.TrimSpace(string(raw)), "cached", nil
		}
	}

	srcPath := ""
	tmpPath := ""
	if p, ok := s.LocalPath(f); ok {
		srcPath = p
	} else if strings.EqualFold(f.StorageBackend, string(StorageBackendS3)) && strings.TrimSpace(f.StorageKey) != "" {
		if err := os.MkdirAll(s.tmpDir(), 0o755); err != nil {
			return "", "", err
		}
		tmpPath = filepath.Join(s.tmpDir(), fmt.Sprintf("%s.src", fileID))
		rc, _, _, _, err := s.GetOriginalObject(ctx, f.StorageBucket, f.StorageKey)
		if err != nil {
			return "", "", err
		}
		raw, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return "", "", err
		}
		if err := os.WriteFile(tmpPath, raw, 0o600); err != nil {
			return "", "", err
		}
		srcPath = tmpPath
	} else {
		return "", "", fmt.Errorf("parse not supported")
	}

	txt, parser, err := ParseToText(ctx, srcPath)
	if err != nil {
		_ = s.UpdateParseFailed(ctx, fileID, err.Error())
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
		return "", parser, err
	}
	if tmpPath != "" {
		_ = os.Remove(tmpPath)
	}
	if err := os.MkdirAll(s.filesTextDir(), 0o755); err != nil {
		return "", parser, err
	}
	rel := fileID + ".txt"
	outPath := filepath.Join(s.filesTextDir(), rel)
	if err := os.WriteFile(outPath, []byte(txt), 0o600); err != nil {
		return "", parser, err
	}
	_ = s.UpdateParsedText(ctx, fileID, rel)
	return strings.TrimSpace(txt), parser, nil
}

func (s *FileStore) s3Client(ctx context.Context) (*s3.Client, error) {
	if s == nil {
		return nil, fmt.Errorf("nil service")
	}
	if !s.Cfg.OSS.Enabled {
		return nil, fmt.Errorf("oss disabled")
	}
	if strings.TrimSpace(s.Cfg.OSS.Endpoint) == "" || strings.TrimSpace(s.Cfg.OSS.Bucket) == "" {
		return nil, fmt.Errorf("oss endpoint/bucket required")
	}
	region := strings.TrimSpace(s.Cfg.OSS.Region)
	if region == "" {
		region = "oss-default"
	}
	ep := strings.TrimSpace(s.Cfg.OSS.Endpoint)

	// Aliyun OSS S3 compatibility typically requires custom endpoint.
	resolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, _ ...any) (aws.Endpoint, error) {
		if service == s3.ServiceID {
			return aws.Endpoint{
				URL:               ep,
				SigningRegion:     region,
				HostnameImmutable: true,
			}, nil
		}
		return aws.Endpoint{}, &aws.EndpointNotFoundError{}
	})

	opts := []func(*config.LoadOptions) error{
		config.WithRegion(region),
		config.WithEndpointResolverWithOptions(resolver),
	}
	if ak := strings.TrimSpace(s.Cfg.OSS.AccessKey); ak != "" {
		opts = append(opts, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			ak, strings.TrimSpace(s.Cfg.OSS.SecretKey), "",
		)))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = s.Cfg.OSS.ForcePathStyle
	}), nil
}

func (s *FileStore) PutOriginalObject(ctx context.Context, key string, body io.Reader, contentType string) (etag string, err error) {
	cli, err := s.s3Client(ctx)
	if err != nil {
		return "", err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("key required")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	out, err := cli.PutObject(cctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.Cfg.OSS.Bucket),
		Key:         aws.String(key),
		Body:        body,
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", err
	}
	return strings.Trim(strings.TrimSpace(aws.ToString(out.ETag)), `"`), nil
}

func (s *FileStore) GetOriginalObject(ctx context.Context, bucket, key string) (rc io.ReadCloser, contentType string, size int64, etag string, err error) {
	cli, err := s.s3Client(ctx)
	if err != nil {
		return nil, "", 0, "", err
	}
	if strings.TrimSpace(bucket) == "" {
		bucket = s.Cfg.OSS.Bucket
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	out, err := cli.GetObject(cctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, "", 0, "", err
	}
	ct := strings.TrimSpace(aws.ToString(out.ContentType))
	if ct == "" {
		ct = "application/octet-stream"
	}
	return out.Body, ct, aws.ToInt64(out.ContentLength), strings.Trim(strings.TrimSpace(aws.ToString(out.ETag)), `"`), nil
}

// RemoveStored deletes a file's stored bytes: the original and the extracted
// text for a local-backed file, the stored object for an S3-backed one. A
// record whose bytes are already gone is not an error — deletion must be
// repeatable over a file whose row outlived its storage.
func (s *FileStore) RemoveStored(ctx context.Context, f File) error {
	if s == nil {
		return fmt.Errorf("nil service")
	}
	if p, ok := s.ParsedTextLocalPath(&f); ok {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	switch {
	case strings.EqualFold(f.StorageBackend, string(StorageBackendLocal)):
		if p, ok := s.LocalPath(&f); ok {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	case strings.EqualFold(f.StorageBackend, string(StorageBackendS3)):
		key := strings.TrimSpace(f.StorageKey)
		if key == "" {
			return nil
		}
		bucket := strings.TrimSpace(f.StorageBucket)
		if bucket == "" {
			bucket = s.Cfg.OSS.Bucket
		}
		cli, err := s.s3Client(ctx)
		if err != nil {
			return err
		}
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		_, err = cli.DeleteObject(cctx, &s3.DeleteObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		})
		return err
	}
	return nil
}

// FileRefInfo holds the file reference metadata from a file_reference part.
type FileRefInfo struct {
	FileID   string
	Label    string
	MIMEType string
}

// TranscriptEntry pairs a parsed message with its file reference infos
// extracted from the raw PartsJSON.
type TranscriptEntry struct {
	Message  llm.Message
	FileRefs []FileRefInfo
}

// ExtractFileRefInfos parses raw parts JSON and returns only the
// file_reference entries. Returns nil for empty or malformed JSON.
func ExtractFileRefInfos(partsJSON string) []FileRefInfo {
	return fileEntries(partsJSON, PartTypeFileReference)
}

// MessageAttachments lists everything a user message attached, in the order
// it was attached: the files it shows the model (file_reference) and the files
// it names in its text (attachment).
func MessageAttachments(partsJSON string) []FileRefInfo {
	return fileEntries(partsJSON, PartTypeFileReference, PartTypeAttachment)
}

func fileEntries(partsJSON string, types ...string) []FileRefInfo {
	raw := strings.TrimSpace(partsJSON)
	if raw == "" || raw == "[]" {
		return nil
	}
	var parts []map[string]any
	if err := json.Unmarshal([]byte(raw), &parts); err != nil {
		return nil
	}
	var refs []FileRefInfo
	for _, part := range parts {
		if !slices.Contains(types, strings.TrimSpace(stringField(part, "type"))) {
			continue
		}
		refs = append(refs, FileRefInfo{
			FileID:   strings.TrimSpace(stringField(part, "file_id")),
			Label:    strings.TrimSpace(stringField(part, "label")),
			MIMEType: strings.TrimSpace(stringField(part, "mime_type")),
		})
	}
	return refs
}

// ListTranscriptMessagesWithRefs reads the session transcript and returns each
// message paired with its file_reference infos extracted from the raw PartsJSON.
// This preserves file_id which ParseMessageParts discards when collapsing
// file_reference parts into text placeholders.
func (s *SessionStore) ListTranscriptMessagesWithRefs(ctx context.Context, sessionID string, limit int) ([]TranscriptEntry, error) {
	rows, err := s.listTranscriptStoredMessages(ctx, s.db, sessionID, limit)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []TranscriptEntry{}, nil
	}
	out := make([]TranscriptEntry, 0, len(rows))
	for _, row := range rows {
		// Skip compact-boundary marker rows (same logic as ListTranscriptMessages).
		if _, isBoundary := ParseCompactBoundaryPart(row.PartsJSON); isBoundary {
			continue
		}
		msg, ok := ParseMessage(row.Role, row.Content, row.PartsJSON)
		if !ok {
			continue
		}
		out = append(out, TranscriptEntry{
			Message:  msg,
			FileRefs: ExtractFileRefInfos(row.PartsJSON),
		})
	}
	return out, nil
}

type OSSConfig struct {
	Enabled        bool
	Endpoint       string
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
	ForcePathStyle bool
}

type Config struct {
	DefaultStorageBackend StorageBackend
	FilesDirRel           string
	FilesTextDirRel       string
	TmpDirRel             string
	MaxUploadBytes        int64

	OSS OSSConfig
}

func LoadConfigFromEnv() Config {
	var cfg Config
	cfg.DefaultStorageBackend = StorageBackendLocal
	cfg.FilesDirRel = "files"
	cfg.FilesTextDirRel = "files-text"
	cfg.TmpDirRel = "tmp"
	cfg.MaxUploadBytes = 50 * 1024 * 1024

	if v := strings.TrimSpace(os.Getenv("FOREBRAIN_MAX_UPLOAD_BYTES")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			cfg.MaxUploadBytes = n
		}
	}

	cfg.OSS.Enabled = strings.EqualFold(strings.TrimSpace(os.Getenv("FOREBRAIN_OSS_ENABLED")), "true")
	cfg.OSS.Endpoint = strings.TrimSpace(os.Getenv("FOREBRAIN_OSS_ENDPOINT"))
	cfg.OSS.Region = strings.TrimSpace(os.Getenv("FOREBRAIN_OSS_REGION"))
	cfg.OSS.Bucket = strings.TrimSpace(os.Getenv("FOREBRAIN_OSS_BUCKET"))
	cfg.OSS.AccessKey = strings.TrimSpace(os.Getenv("FOREBRAIN_OSS_ACCESS_KEY"))
	cfg.OSS.SecretKey = strings.TrimSpace(os.Getenv("FOREBRAIN_OSS_SECRET_KEY"))
	cfg.OSS.ForcePathStyle = strings.EqualFold(strings.TrimSpace(os.Getenv("FOREBRAIN_OSS_FORCE_PATH_STYLE")), "true")

	if cfg.OSS.Enabled {
		cfg.DefaultStorageBackend = StorageBackendS3
	}

	return cfg
}

type StorageBackend string

const (
	StorageBackendLocal StorageBackend = "local"
	StorageBackendS3    StorageBackend = "s3"
)

type ParseStatus string

const (
	ParseStatusPending ParseStatus = "pending"
	ParseStatusRunning ParseStatus = "running"
	ParseStatusDone    ParseStatus = "done"
	ParseStatusFailed  ParseStatus = "failed"
)

type File struct {
	ID             string `json:"id"`
	SessionID      string `json:"session_id"`
	OriginalName   string `json:"original_name"`
	MediaType      string `json:"media_type"`
	SizeBytes      int64  `json:"size_bytes"`
	SHA256         string `json:"sha256"`
	StorageBackend string `json:"storage_backend"`
	StorageBucket  string `json:"storage_bucket"`
	StorageKey     string `json:"storage_key"`
	ParseStatus    string `json:"parse_status"`
	ParsedTextPath string `json:"parsed_text_path"`
	ParseError     string `json:"parse_error"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

func ParseToText(ctx context.Context, absPath string) (text string, parser string, err error) {
	_ = ctx
	absPath = strings.TrimSpace(absPath)
	if absPath == "" {
		return "", "", fmt.Errorf("path required")
	}
	// Go-native text extraction (best-effort): read the file as UTF-8 text.
	raw, rerr := os.ReadFile(absPath)
	if rerr != nil {
		return "", "go_native", rerr
	}
	return strings.TrimSpace(string(raw)), "go_native", nil
}
