package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/alikilicw/filecosystem-be/internal/contracts"
	"github.com/alikilicw/filecosystem-be/internal/domain"
	"github.com/alikilicw/filecosystem-be/internal/queue"
	"github.com/alikilicw/filecosystem-be/internal/repository"
	"github.com/alikilicw/filecosystem-be/internal/storage"
	"github.com/google/uuid"
)

const (
	defaultLossyQuality = 82
	compressQuality     = 75
	maxDimension        = 12000
)

type CreateJobInput struct {
	FileID    uuid.UUID
	Operation contracts.Operation
	Params    contracts.ImageParams
}

// JobView is the API representation of a job: the stored row plus freshly
// signed URLs once a result exists. The download URL carries an attachment
// disposition, so a separate preview URL is needed to show the result inline.
type JobView struct {
	domain.Job
	DownloadURL string `json:"download_url,omitempty"`
	PreviewURL  string `json:"preview_url,omitempty"`
}

type ImageService struct {
	files   *repository.FileRepository
	jobs    *repository.JobRepository
	queue   *queue.Client
	storage *storage.Storage
	log     *slog.Logger
}

func NewImageService(
	files *repository.FileRepository,
	jobs *repository.JobRepository,
	q *queue.Client,
	store *storage.Storage,
	log *slog.Logger,
) *ImageService {
	return &ImageService{files: files, jobs: jobs, queue: q, storage: store, log: log}
}

func (s *ImageService) CreateJob(ctx context.Context, in CreateJobInput) (domain.Job, error) {
	file, err := s.files.Get(ctx, in.FileID)
	if err != nil {
		return domain.Job{}, err
	}
	// A file the client has not reported uploading may have no object behind it
	// at all, so it must not reach a worker.
	if !file.IsUsable() {
		return domain.Job{}, fmt.Errorf("%w: file %s is %s, not ready", domain.ErrInvalidInput, file.ID, file.Status)
	}
	if file.Kind != contracts.KindImage {
		return domain.Job{}, fmt.Errorf("%w: file %s is not an image", domain.ErrInvalidInput, file.ID)
	}

	params, err := normalizeParams(in.Operation, in.Params, file.MimeType)
	if err != nil {
		return domain.Job{}, err
	}

	job := domain.Job{
		ID:        uuid.New(),
		FileID:    file.ID,
		Kind:      contracts.KindImage,
		Operation: in.Operation,
		Status:    contracts.StatusQueued,
		Params:    params,
		CreatedAt: time.Now().UTC(),
	}
	job.UpdatedAt = job.CreatedAt

	if err := s.jobs.Create(ctx, job); err != nil {
		return domain.Job{}, err
	}

	msg := contracts.JobMessage{
		JobID:      job.ID.String(),
		FileID:     file.ID.String(),
		Kind:       contracts.KindImage,
		Operation:  job.Operation,
		SourceKey:  file.StorageKey,
		SourceName: file.OriginalName,
		SourceMime: file.MimeType,
		Params:     params,
		CreatedAt:  job.CreatedAt,
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return domain.Job{}, fmt.Errorf("marshal job message: %w", err)
	}

	if err := s.queue.Publish(ctx, contracts.ExchangeJobs, contracts.RoutingKeyImage, body); err != nil {
		if markErr := s.jobs.MarkFailed(ctx, job.ID, "job could not be queued"); markErr != nil {
			s.log.Error("failed to mark unqueued job", "job_id", job.ID, "error", markErr)
		}
		return domain.Job{}, fmt.Errorf("publish job: %w", err)
	}
	return job, nil
}

func (s *ImageService) GetJob(ctx context.Context, id uuid.UUID) (JobView, error) {
	job, err := s.jobs.Get(ctx, id)
	if err != nil {
		return JobView{}, err
	}
	return s.view(ctx, job), nil
}

func (s *ImageService) ListJobsByFile(ctx context.Context, fileID uuid.UUID) ([]JobView, error) {
	jobs, err := s.jobs.ListByFile(ctx, fileID)
	if err != nil {
		return nil, err
	}
	views := make([]JobView, 0, len(jobs))
	for _, job := range jobs {
		views = append(views, s.view(ctx, job))
	}
	return views, nil
}

// DownloadURL links to the result of a finished job.
func (s *ImageService) DownloadURL(ctx context.Context, id uuid.UUID) (string, error) {
	job, err := s.jobs.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if job.Status != contracts.StatusSucceeded || job.ResultKey == "" {
		return "", fmt.Errorf("%w: job %s has no result yet", domain.ErrInvalidInput, id)
	}
	return s.storage.ObjectURL(ctx, job.ResultKey, job.ResultName)
}

func (s *ImageService) view(ctx context.Context, job domain.Job) JobView {
	v := JobView{Job: job}
	if job.Status != contracts.StatusSucceeded || job.ResultKey == "" {
		return v
	}

	if url, err := s.storage.ObjectURL(ctx, job.ResultKey, job.ResultName); err != nil {
		s.log.Error("failed to build download url", "job_id", job.ID, "error", err)
	} else {
		v.DownloadURL = url
	}

	if url, err := s.storage.ObjectURL(ctx, job.ResultKey, ""); err != nil {
		s.log.Error("failed to build preview url", "job_id", job.ID, "error", err)
	} else {
		v.PreviewURL = url
	}
	return v
}

// normalizeParams fills in defaults and rejects combinations the workers cannot
// execute, so an invalid request fails at the HTTP edge instead of in a worker.
func normalizeParams(op contracts.Operation, p contracts.ImageParams, sourceMime string) (contracts.ImageParams, error) {
	out := contracts.ImageParams{StripMetadata: p.StripMetadata}

	fallbackFormat := contracts.FormatFromMime(sourceMime)
	if fallbackFormat == "" {
		// GIF, BMP and TIFF inputs cannot be re-encoded as themselves, so PNG
		// is used as the lossless default.
		fallbackFormat = contracts.FormatPNG
	}

	switch op {
	case contracts.OpConvert:
		if p.Format == "" {
			return out, fmt.Errorf("%w: format is required for convert", domain.ErrInvalidInput)
		}
		format, _, ok := resolveFormat(p.Format)
		if !ok {
			return out, fmt.Errorf("%w: unsupported target format %q", domain.ErrUnsupported, p.Format)
		}
		out.Format = format
		out.Quality = clampQuality(p.Quality, defaultLossyQuality)

	case contracts.OpResize:
		if p.Width <= 0 && p.Height <= 0 {
			return out, fmt.Errorf("%w: width or height is required for resize", domain.ErrInvalidInput)
		}
		if p.Width > maxDimension || p.Height > maxDimension {
			return out, fmt.Errorf("%w: dimensions may not exceed %d px", domain.ErrInvalidInput, maxDimension)
		}
		if p.Width < 0 || p.Height < 0 {
			return out, fmt.Errorf("%w: dimensions may not be negative", domain.ErrInvalidInput)
		}
		fit := p.Fit
		if fit == "" {
			fit = contracts.FitContain
		}
		if fit != contracts.FitContain && fit != contracts.FitCover && fit != contracts.FitFill {
			return out, fmt.Errorf("%w: unsupported fit %q", domain.ErrInvalidInput, p.Fit)
		}
		if fit != contracts.FitContain && (p.Width <= 0 || p.Height <= 0) {
			return out, fmt.Errorf("%w: fit %q requires both width and height", domain.ErrInvalidInput, fit)
		}
		out.Width, out.Height, out.Fit = p.Width, p.Height, fit
		out.Format, out.Quality = resolveOutputFormat(p.Format, fallbackFormat, p.Quality)
		if out.Format == "" {
			return out, fmt.Errorf("%w: unsupported target format %q", domain.ErrUnsupported, p.Format)
		}

	case contracts.OpCompress:
		out.Format, out.Quality = resolveOutputFormat(p.Format, fallbackFormat, p.Quality)
		if out.Format == "" {
			return out, fmt.Errorf("%w: unsupported target format %q", domain.ErrUnsupported, p.Format)
		}
		if p.Quality == 0 {
			out.Quality = compressQuality
		}
		out.StripMetadata = true

	default:
		return out, fmt.Errorf("%w: unknown operation %q", domain.ErrUnsupported, op)
	}

	return out, nil
}

func resolveOutputFormat(requested, fallback string, quality int) (string, int) {
	if requested == "" {
		return fallback, clampQuality(quality, defaultLossyQuality)
	}
	format, _, ok := resolveFormat(requested)
	if !ok {
		return "", 0
	}
	return format, clampQuality(quality, defaultLossyQuality)
}

func resolveFormat(format string) (string, string, bool) {
	_, mime, ok := contracts.FormatInfo(format)
	if !ok {
		return "", "", false
	}
	return contracts.FormatFromMime(mime), mime, true
}

func clampQuality(quality, fallback int) int {
	if quality <= 0 {
		return fallback
	}
	if quality > 100 {
		return 100
	}
	return quality
}
