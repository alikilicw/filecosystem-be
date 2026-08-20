package service

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/alikilicw/filecosystem-be/internal/contracts"
	"github.com/alikilicw/filecosystem-be/internal/domain"
	"github.com/alikilicw/filecosystem-be/internal/repository"
	"github.com/alikilicw/filecosystem-be/internal/storage"
	"github.com/google/uuid"
)

// FileService runs the direct-to-S3 upload handshake. Bytes never pass through
// this process: it hands out a presigned URL, records what the client claimed it
// would upload, and marks the file usable once the client reports it is done.
// Nothing ever inspects the stored object, so the recorded metadata stays the
// client's word.
type FileService struct {
	files     *repository.FileRepository
	storage   *storage.Storage
	log       *slog.Logger
	maxSize   int64
	ttl       time.Duration
	uploadTTL time.Duration
}

func NewFileService(
	files *repository.FileRepository,
	store *storage.Storage,
	log *slog.Logger,
	maxSize int64,
	ttl, uploadTTL time.Duration,
) *FileService {
	return &FileService{
		files:     files,
		storage:   store,
		log:       log,
		maxSize:   maxSize,
		ttl:       ttl,
		uploadTTL: uploadTTL,
	}
}

func (s *FileService) MaxSize() int64 { return s.maxSize }

// CreateUploadInput is what the client says it is about to upload. It decides
// whether an upload URL is worth issuing at all and what the storage key looks
// like, and it is also what gets recorded: nothing checks it afterwards.
type CreateUploadInput struct {
	Filename    string
	ContentType string
	SizeBytes   int64
}

// CreateUpload reserves a file and returns the presigned URL the client PUTs the
// bytes to. The checks here are the only ones the platform makes, and they run
// against the client's claims; the signature then pins the key and content type
// so the upload can at least go nowhere else.
func (s *FileService) CreateUpload(ctx context.Context, in CreateUploadInput) (domain.File, storage.PresignedUpload, error) {
	if in.SizeBytes <= 0 {
		return domain.File{}, storage.PresignedUpload{}, fmt.Errorf("%w: size_bytes must be positive", domain.ErrInvalidInput)
	}
	if in.SizeBytes > s.maxSize {
		return domain.File{}, storage.PresignedUpload{}, fmt.Errorf("%w: file exceeds %d bytes", domain.ErrInvalidInput, s.maxSize)
	}

	kind, ext, ok := contracts.InputMime(in.ContentType)
	if !ok {
		return domain.File{}, storage.PresignedUpload{}, fmt.Errorf("%w: %s is not a supported file type", domain.ErrUnsupported, in.ContentType)
	}

	now := time.Now().UTC()
	id := uuid.New()
	name := sanitizeName(in.Filename, ext)
	key := fmt.Sprintf("uploads/%s/%s", id, name)

	// The content type is pinned into the signature, so it has to be the
	// normalised form the object will actually be stored with.
	contentType := canonicalMime(in.ContentType)

	// Presigning is local arithmetic, so doing it before the insert means a
	// failure here leaves nothing behind to clean up.
	upload, err := s.storage.PresignPut(ctx, key, contentType, s.uploadTTL)
	if err != nil {
		return domain.File{}, storage.PresignedUpload{}, err
	}

	file := domain.File{
		ID:           id,
		OriginalName: name,
		StorageKey:   key,
		MimeType:     contentType,
		SizeBytes:    in.SizeBytes,
		Kind:         kind,
		Status:       contracts.FileStatusPending,
		CreatedAt:    now,
		UpdatedAt:    now,
		ExpiresAt:    now.Add(s.ttl),
	}
	if err := s.files.Create(ctx, file); err != nil {
		return domain.File{}, storage.PresignedUpload{}, err
	}
	return file, upload, nil
}

// CompleteUpload is the client reporting that its S3 upload finished, which is
// the only signal the API gets that an object appeared, and the only thing that
// makes a file usable. Calling it more than once is safe.
func (s *FileService) CompleteUpload(ctx context.Context, id uuid.UUID) (domain.File, error) {
	file, err := s.files.Get(ctx, id)
	if err != nil {
		return domain.File{}, err
	}
	if file.Status != contracts.FileStatusPending {
		return file, nil
	}

	if _, err := s.files.MarkReady(ctx, id); err != nil {
		return domain.File{}, err
	}
	return s.files.Get(ctx, id)
}

func (s *FileService) Get(ctx context.Context, id uuid.UUID) (domain.File, error) {
	return s.files.Get(ctx, id)
}

// PreviewURL signs the stored original. It refuses a file the client has not
// reported uploading, since there may be no object behind the key at all.
func (s *FileService) PreviewURL(ctx context.Context, file domain.File) (string, error) {
	if !file.IsUsable() {
		return "", fmt.Errorf("%w: file %s is not ready", domain.ErrInvalidInput, file.ID)
	}
	return s.storage.PresignGet(ctx, file.StorageKey, "")
}

// PurgeExpired drops files past their TTL along with their derived results.
func (s *FileService) PurgeExpired(ctx context.Context, limit int) (int, error) {
	keys, err := s.files.DeleteExpired(ctx, limit)
	if err != nil {
		return 0, err
	}
	return s.purge(ctx, keys), nil
}

// PurgeAbandoned collects what the handshake leaves behind: upload URLs nobody
// used, and uploads nobody reported. One expired URL lifetime is the cutoff —
// past that the client can no longer upload, so a file still pending never will
// be claimed.
func (s *FileService) PurgeAbandoned(ctx context.Context, limit int) (int, error) {
	cutoff := time.Now().UTC().Add(-s.uploadTTL)
	keys, err := s.files.DeleteAbandoned(ctx, cutoff, limit)
	if err != nil {
		return 0, err
	}
	return s.purge(ctx, keys), nil
}

// purge deletes objects best-effort. A key that was never uploaded against has
// no object behind it, and S3 treats deleting a missing key as success, so both
// cases fall out the same way.
func (s *FileService) purge(ctx context.Context, keys []string) int {
	removed := 0
	for _, key := range keys {
		if err := s.storage.Delete(ctx, key); err != nil {
			s.log.Warn("failed to purge object", "key", key, "error", err)
			continue
		}
		removed++
	}
	return removed
}

// canonicalMime strips parameters and folds case so the stored content type
// matches what the signature pinned.
func canonicalMime(mime string) string {
	if idx := strings.IndexByte(mime, ';'); idx >= 0 {
		mime = mime[:idx]
	}
	return strings.ToLower(strings.TrimSpace(mime))
}

// sanitizeName strips any directory component and normalises the extension so
// a hostile filename cannot escape its storage prefix.
func sanitizeName(name, fallbackExt string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(name)

	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}

	cleaned := strings.Trim(b.String(), ".-")
	if cleaned == "" {
		cleaned = "file" + fallbackExt
	}
	if len(cleaned) > 120 {
		cleaned = cleaned[len(cleaned)-120:]
	}
	if path.Ext(cleaned) == "" {
		cleaned += fallbackExt
	}
	return cleaned
}
