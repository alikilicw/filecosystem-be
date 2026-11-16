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

const defaultLossyQuality = 82

type CreateJobInput struct {
	FileID    uuid.UUID
	Operation contracts.Operation
	Params    contracts.ImageParams
}

// JobView is the API representation of a job: the stored row plus a link to
// the result once one exists. The link opens the result in the browser; to
// save it, clients use GET /api/v1/jobs/{id}/download.
type JobView struct {
	domain.Job
	ResultURL string `json:"result_url,omitempty"`
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

// DownloadURL links to the result of a finished job so the browser saves it.
func (s *ImageService) DownloadURL(ctx context.Context, id uuid.UUID) (string, error) {
	job, err := s.jobs.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if job.Status != contracts.StatusSucceeded || job.ResultKey == "" {
		return "", fmt.Errorf("%w: job %s has no result yet", domain.ErrInvalidInput, id)
	}
	return s.storage.DownloadURL(ctx, job.ResultKey, job.ResultName)
}

func (s *ImageService) view(ctx context.Context, job domain.Job) JobView {
	v := JobView{Job: job}
	if job.Status != contracts.StatusSucceeded || job.ResultKey == "" {
		return v
	}

	if url, err := s.storage.ObjectURL(ctx, job.ResultKey); err != nil {
		s.log.Error("failed to build result url", "job_id", job.ID, "error", err)
	} else {
		v.ResultURL = url
	}
	return v
}

// normalizeParams fills in defaults and rejects combinations the workers cannot
// execute, so an invalid request fails at the HTTP edge instead of in a worker.
// The editor sends every export as a pipeline job, so that is the only
// operation there is.
func normalizeParams(op contracts.Operation, p contracts.ImageParams, sourceMime string) (contracts.ImageParams, error) {
	if op != contracts.OpPipeline {
		return contracts.ImageParams{}, fmt.Errorf("%w: unknown operation %q", domain.ErrUnsupported, op)
	}

	fallbackFormat := contracts.FormatFromMime(sourceMime)
	if fallbackFormat == "" {
		// GIF and BMP inputs cannot be re-encoded as themselves, so PNG is used
		// as the lossless default.
		fallbackFormat = contracts.FormatPNG
	}
	return normalizePipeline(p, fallbackFormat)
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
