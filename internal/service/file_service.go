package service

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"time"

	// Registered for their DecodeConfig side effects so uploads report dimensions.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	"github.com/alikilicw/filecosystem-be/internal/contracts"
	"github.com/alikilicw/filecosystem-be/internal/domain"
	"github.com/alikilicw/filecosystem-be/internal/repository"
	"github.com/alikilicw/filecosystem-be/internal/storage"
	"github.com/google/uuid"
)

// sniffLen is what http.DetectContentType inspects; reading exactly this much
// keeps the sniff buffer small while covering every supported signature.
const sniffLen = 512

var supportedImageMimes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
	"image/bmp":  ".bmp",
	"image/tiff": ".tiff",
}

type FileService struct {
	files   *repository.FileRepository
	storage *storage.Storage
	log     *slog.Logger
	maxSize int64
	ttl     time.Duration
}

func NewFileService(files *repository.FileRepository, store *storage.Storage, log *slog.Logger, maxSize int64, ttl time.Duration) *FileService {
	return &FileService{files: files, storage: store, log: log, maxSize: maxSize, ttl: ttl}
}

func (s *FileService) MaxSize() int64 { return s.maxSize }

// Upload validates the incoming part by content sniffing rather than trusting
// the client-supplied name or content type, then persists it.
func (s *FileService) Upload(ctx context.Context, header *multipart.FileHeader) (domain.File, error) {
	if header.Size <= 0 {
		return domain.File{}, fmt.Errorf("%w: empty file", domain.ErrInvalidInput)
	}
	if header.Size > s.maxSize {
		return domain.File{}, fmt.Errorf("%w: file exceeds %d bytes", domain.ErrInvalidInput, s.maxSize)
	}

	src, err := header.Open()
	if err != nil {
		return domain.File{}, fmt.Errorf("open upload: %w", err)
	}
	defer func() { _ = src.Close() }()

	sniff := make([]byte, sniffLen)
	n, err := io.ReadFull(src, sniff)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return domain.File{}, fmt.Errorf("read upload: %w", err)
	}
	sniff = sniff[:n]

	mimeType := http.DetectContentType(sniff)
	if idx := strings.IndexByte(mimeType, ';'); idx >= 0 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}

	ext, ok := supportedImageMimes[mimeType]
	if !ok {
		return domain.File{}, fmt.Errorf("%w: %s is not a supported image type", domain.ErrUnsupported, mimeType)
	}

	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return domain.File{}, fmt.Errorf("rewind upload: %w", err)
	}

	width, height := imageDimensions(sniff, src)
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return domain.File{}, fmt.Errorf("rewind upload: %w", err)
	}

	now := time.Now().UTC()
	id := uuid.New()
	name := sanitizeName(header.Filename, ext)
	key := fmt.Sprintf("uploads/%s/%s", id, name)

	if err := s.storage.Put(ctx, key, src, header.Size, mimeType); err != nil {
		return domain.File{}, err
	}

	file := domain.File{
		ID:           id,
		OriginalName: name,
		StorageKey:   key,
		MimeType:     mimeType,
		SizeBytes:    header.Size,
		Kind:         contracts.KindImage,
		Width:        width,
		Height:       height,
		CreatedAt:    now,
		ExpiresAt:    now.Add(s.ttl),
	}

	if err := s.files.Create(ctx, file); err != nil {
		if delErr := s.storage.Delete(ctx, key); delErr != nil {
			s.log.Error("orphaned object left in storage", "key", key, "error", delErr)
		}
		return domain.File{}, err
	}
	return file, nil
}

func (s *FileService) Get(ctx context.Context, id uuid.UUID) (domain.File, error) {
	return s.files.Get(ctx, id)
}

func (s *FileService) PreviewURL(ctx context.Context, file domain.File) (string, error) {
	return s.storage.PresignGet(ctx, file.StorageKey, "")
}

// PurgeExpired drops files past their TTL along with their derived results.
func (s *FileService) PurgeExpired(ctx context.Context, limit int) (int, error) {
	keys, err := s.files.DeleteExpired(ctx, limit)
	if err != nil {
		return 0, err
	}
	for _, key := range keys {
		if err := s.storage.Delete(ctx, key); err != nil {
			s.log.Warn("failed to purge object", "key", key, "error", err)
		}
	}
	return len(keys), nil
}

func imageDimensions(head []byte, rest io.Reader) (int, int) {
	cfg, _, err := image.DecodeConfig(io.MultiReader(bytes.NewReader(head), rest))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
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
