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
	// lockTTL is how long one operation may hold a session. It is longer than
	// the worker's own job timeout, so a lock only lapses when a job was lost.
	lockTTL = 12 * time.Minute

	// failedMessage is what a user sees when a worker fails; the worker's own
	// error is logged and kept on the job, not shown.
	failedMessage = "The image could not be processed."
)

// SessionService runs image editing sessions. Every operation is done by a
// worker; the API only validates, records versions and moves the head.
type SessionService struct {
	files    *repository.FileRepository
	sessions *repository.SessionRepository
	jobs     *repository.JobRepository
	queue    *queue.Client
	storage  *storage.Storage
	log      *slog.Logger
}

func NewSessionService(
	files *repository.FileRepository,
	sessions *repository.SessionRepository,
	jobs *repository.JobRepository,
	q *queue.Client,
	store *storage.Storage,
	log *slog.Logger,
) *SessionService {
	return &SessionService{files: files, sessions: sessions, jobs: jobs, queue: q, storage: store, log: log}
}

// EditInput is one operation: at most one step, and any change to how the
// result is written. At least one of them must be set.
type EditInput struct {
	Step   *contracts.ImageStep
	Encode *EncodeChange
}

// Create opens an upload for editing. Version 0 is queued at once and the
// session stays locked until the worker has read the original.
func (s *SessionService) Create(ctx context.Context, fileID uuid.UUID) (SessionView, error) {
	file, err := s.files.Get(ctx, fileID)
	if err != nil {
		return SessionView{}, err
	}
	// A file the client has not reported uploading may have no object behind it
	// at all, so it must not reach a worker.
	if !file.IsUsable() {
		return SessionView{}, fmt.Errorf("%w: file %s is %s, not ready", domain.ErrInvalidInput, file.ID, file.Status)
	}
	if file.Kind != contracts.KindImage {
		return SessionView{}, fmt.Errorf("%w: file %s is not an image", domain.ErrInvalidInput, file.ID)
	}

	now := time.Now().UTC()
	session := domain.Session{
		ID:            uuid.New(),
		FileID:        file.ID,
		NextSeq:       1,
		LockExpiresAt: now.Add(lockTTL),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	job := domain.Job{
		ID:        uuid.New(),
		FileID:    file.ID,
		SessionID: session.ID,
		Seq:       0,
		Kind:      contracts.KindImage,
		Operation: contracts.OpSource,
		Status:    contracts.StatusQueued,
		CreatedAt: now,
		UpdatedAt: now,
	}
	session.SourceID, session.HeadID, session.LockJobID = job.ID, job.ID, job.ID

	if err := s.jobs.Create(ctx, job); err != nil {
		return SessionView{}, err
	}
	if err := s.sessions.Create(ctx, session); err != nil {
		return SessionView{}, err
	}

	msg := s.message(file, session, job)
	msg.InputKey = file.StorageKey
	if err := s.publish(ctx, session, job, msg); err != nil {
		return SessionView{}, err
	}
	return s.Get(ctx, session.ID)
}

// Edit makes a new version from the head. It fails with ErrBusy while
// another operation runs.
func (s *SessionService) Edit(ctx context.Context, id uuid.UUID, in EditInput) (SessionView, error) {
	var step *contracts.ImageStep
	if in.Step != nil {
		clean, err := normalizeStep(*in.Step)
		if err != nil {
			return SessionView{}, fmt.Errorf("%w: %s: %v", domain.ErrInvalidInput, in.Step.Op, err)
		}
		step = &clean
	}

	session, err := s.sessions.Get(ctx, id)
	if err != nil {
		return SessionView{}, err
	}
	if session.Locked(time.Now()) {
		return SessionView{}, domain.ErrBusy
	}
	head, err := s.jobs.Get(ctx, session.HeadID)
	if err != nil {
		return SessionView{}, err
	}
	if head.Status != contracts.StatusSucceeded || head.Encode == nil {
		return SessionView{}, fmt.Errorf("%w: the image could not be opened", domain.ErrInvalidInput)
	}

	enc, err := mergeEncode(*head.Encode, in.Encode)
	if err != nil {
		return SessionView{}, err
	}
	if step == nil && enc == *head.Encode {
		return SessionView{}, fmt.Errorf("%w: nothing to change", domain.ErrInvalidInput)
	}

	file, err := s.files.Get(ctx, session.FileID)
	if err != nil {
		return SessionView{}, err
	}

	jobID := uuid.New()
	locked, err := s.sessions.Acquire(ctx, id, jobID, lockTTL)
	if err != nil {
		return SessionView{}, err
	}
	if locked.HeadID != head.ID {
		// Undo or another edit moved the head between the read and the lock.
		s.release(ctx, id, jobID, "")
		return SessionView{}, domain.ErrBusy
	}

	now := time.Now().UTC()
	job := domain.Job{
		ID:        jobID,
		FileID:    file.ID,
		SessionID: id,
		ParentID:  head.ID,
		Seq:       locked.NextSeq - 1,
		Kind:      contracts.KindImage,
		Operation: contracts.OpEdit,
		Status:    contracts.StatusQueued,
		Step:      step,
		Encode:    &enc,
		Animation: head.Animation,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.jobs.Create(ctx, job); err != nil {
		s.release(ctx, id, jobID, "")
		return SessionView{}, err
	}

	msg := s.message(file, locked, job)
	msg.InputKey = head.IntermediateKey
	msg.Animation = head.Animation
	msg.Step = step
	msg.Encode = &enc
	if err := s.publish(ctx, locked, job, msg); err != nil {
		return SessionView{}, err
	}
	return s.Get(ctx, id)
}

// Undo moves the head back to its parent.
func (s *SessionService) Undo(ctx context.Context, id uuid.UUID) (SessionView, error) {
	return s.moveHead(ctx, id, func(session domain.Session, head domain.Job) (uuid.UUID, error) {
		if head.ParentID == uuid.Nil {
			return uuid.Nil, fmt.Errorf("%w: nothing to undo", domain.ErrInvalidInput)
		}
		return head.ParentID, nil
	})
}

// Reset moves the head back to the original.
func (s *SessionService) Reset(ctx context.Context, id uuid.UUID) (SessionView, error) {
	return s.moveHead(ctx, id, func(session domain.Session, head domain.Job) (uuid.UUID, error) {
		if head.ID == session.SourceID {
			return uuid.Nil, fmt.Errorf("%w: nothing to reset", domain.ErrInvalidInput)
		}
		return session.SourceID, nil
	})
}

func (s *SessionService) moveHead(ctx context.Context, id uuid.UUID, target func(domain.Session, domain.Job) (uuid.UUID, error)) (SessionView, error) {
	session, err := s.sessions.Get(ctx, id)
	if err != nil {
		return SessionView{}, err
	}
	if session.Locked(time.Now()) {
		return SessionView{}, domain.ErrBusy
	}
	head, err := s.jobs.Get(ctx, session.HeadID)
	if err != nil {
		return SessionView{}, err
	}
	to, err := target(session, head)
	if err != nil {
		return SessionView{}, err
	}
	if err := s.sessions.MoveHead(ctx, id, head.ID, to); err != nil {
		return SessionView{}, err
	}
	return s.Get(ctx, id)
}

// DownloadURL links to the head version's file: the upload itself while
// nothing has been changed, otherwise the head's result.
func (s *SessionService) DownloadURL(ctx context.Context, id uuid.UUID) (string, error) {
	session, err := s.sessions.Get(ctx, id)
	if err != nil {
		return "", err
	}
	head, err := s.jobs.Get(ctx, session.HeadID)
	if err != nil {
		return "", err
	}
	if head.Status != contracts.StatusSucceeded || head.ResultKey == "" {
		return "", fmt.Errorf("%w: the image is not ready yet", domain.ErrInvalidInput)
	}
	return s.storage.DownloadURL(ctx, head.ResultKey, head.ResultName)
}

// ApplyResult records a worker's outcome and releases the session: a new
// version becomes the head, a failure leaves the head where it was.
func (s *SessionService) ApplyResult(ctx context.Context, res contracts.JobResult) error {
	job, ok, err := s.jobs.ApplyResult(ctx, res)
	if err != nil || !ok {
		return err
	}
	succeeded := res.Status == contracts.StatusSucceeded
	reason := ""
	if !succeeded {
		reason = failedMessage
		s.log.Warn("image job failed", "job_id", job.ID, "session_id", job.SessionID, "error", res.Error)
	}
	if _, err := s.sessions.Finish(ctx, job.SessionID, job.ID, succeeded, reason); err != nil {
		return err
	}
	return nil
}

func (s *SessionService) message(file domain.File, session domain.Session, job domain.Job) contracts.JobMessage {
	return contracts.JobMessage{
		JobID:        job.ID.String(),
		FileID:       file.ID.String(),
		SessionID:    session.ID.String(),
		Seq:          job.Seq,
		Kind:         contracts.KindImage,
		Operation:    job.Operation,
		SourceKey:    file.StorageKey,
		SourceName:   file.OriginalName,
		SourceMime:   file.MimeType,
		OutputPrefix: fmt.Sprintf("sessions/%s/%s/", session.ID, job.ID),
		CreatedAt:    job.CreatedAt,
	}
}

// publish queues a job. When that fails the job is marked failed and the
// session released, so the user can simply try again.
func (s *SessionService) publish(ctx context.Context, session domain.Session, job domain.Job, msg contracts.JobMessage) error {
	body, err := json.Marshal(msg)
	if err == nil {
		err = s.queue.Publish(ctx, contracts.ExchangeJobs, contracts.RoutingKeyImage, body)
	}
	if err == nil {
		return nil
	}
	if markErr := s.jobs.MarkFailed(ctx, job.ID, "job could not be queued"); markErr != nil {
		s.log.Error("failed to mark unqueued job", "job_id", job.ID, "error", markErr)
	}
	s.release(ctx, session.ID, job.ID, failedMessage)
	return fmt.Errorf("publish job: %w", err)
}

func (s *SessionService) release(ctx context.Context, id, jobID uuid.UUID, reason string) {
	if _, err := s.sessions.Finish(ctx, id, jobID, false, reason); err != nil {
		s.log.Error("failed to release session", "session_id", id, "job_id", jobID, "error", err)
	}
}
