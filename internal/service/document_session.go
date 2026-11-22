package service

import (
	"context"
	"fmt"
	"time"

	"github.com/alikilicw/filecosystem-be/internal/contracts"
	"github.com/alikilicw/filecosystem-be/internal/domain"
	"github.com/google/uuid"
)

// CreateDocument opens one or more files as one document: several files are
// joined in the order given, which is what merging is, and an image becomes a
// page of its own. password opens any of the PDFs that is encrypted; it goes
// to the worker in the job message and is stored nowhere. Version 0 is queued
// at once and the session stays locked until the worker has read every file.
func (s *SessionService) CreateDocument(ctx context.Context, fileIDs []uuid.UUID, password string) (SessionView, error) {
	if len(fileIDs) == 0 {
		return SessionView{}, fmt.Errorf("%w: at least one file is required", domain.ErrInvalidInput)
	}
	if len(fileIDs) > contracts.MaxDocumentInputs {
		return SessionView{}, fmt.Errorf("%w: a document is opened from at most %d files", domain.ErrInvalidInput, contracts.MaxDocumentInputs)
	}
	if len(password) > contracts.MaxPasswordBytes {
		return SessionView{}, fmt.Errorf("%w: a password has at most %d bytes", domain.ErrInvalidInput, contracts.MaxPasswordBytes)
	}
	files, err := s.documentFiles(ctx, fileIDs)
	if err != nil {
		return SessionView{}, err
	}
	first := files[0]

	now := time.Now().UTC()
	session := domain.Session{
		ID:            uuid.New(),
		Kind:          contracts.KindDocument,
		FileID:        first.ID,
		NextSeq:       1,
		LockExpiresAt: now.Add(lockTTL),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	job := domain.Job{
		ID:        uuid.New(),
		FileID:    first.ID,
		SessionID: session.ID,
		Seq:       0,
		Kind:      contracts.KindDocument,
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

	msg := s.message(first, session, job)
	msg.InputKey = first.StorageKey
	msg.Inputs = inputsOf(files)
	for i := range msg.Inputs {
		msg.Inputs[i].Password = password
	}
	if err := s.publish(ctx, session, job, msg); err != nil {
		return SessionView{}, err
	}
	return s.Get(ctx, contracts.KindDocument, session.ID)
}

// EditDocument makes a new version of a document from its head. It fails
// with ErrBusy while another operation runs.
func (s *SessionService) EditDocument(ctx context.Context, id uuid.UUID, step contracts.DocumentStep) (SessionView, error) {
	session, err := s.load(ctx, contracts.KindDocument, id)
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
	if head.Status != contracts.StatusSucceeded || len(head.Pages) == 0 {
		return SessionView{}, fmt.Errorf("%w: the document could not be opened", domain.ErrInvalidInput)
	}

	clean, err := normalizeDocStep(step, len(head.Pages))
	if err != nil {
		return SessionView{}, fmt.Errorf("%w: %s: %v", domain.ErrInvalidInput, step.Op, err)
	}

	var inputs []contracts.Input
	if clean.Op == contracts.DocInsert {
		ids := make([]uuid.UUID, len(clean.Files))
		for i, raw := range clean.Files {
			ids[i] = uuid.MustParse(raw) // normalizeDocStep checked every id
		}
		files, err := s.documentFiles(ctx, ids)
		if err != nil {
			return SessionView{}, err
		}
		inputs = inputsOf(files)
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
		Kind:      contracts.KindDocument,
		Operation: contracts.OpEdit,
		Status:    contracts.StatusQueued,
		DocStep:   &clean,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.jobs.Create(ctx, job); err != nil {
		s.release(ctx, id, jobID, "")
		return SessionView{}, err
	}

	msg := s.message(file, locked, job)
	msg.InputKey = head.IntermediateKey
	msg.DocStep = &clean
	msg.Inputs = inputs
	msg.Pages = head.Pages
	if err := s.publish(ctx, locked, job, msg); err != nil {
		return SessionView{}, err
	}
	return s.Get(ctx, contracts.KindDocument, id)
}

// documentFiles loads the uploads a document is made from, in order, and
// checks that each one is an uploaded PDF or image.
func (s *SessionService) documentFiles(ctx context.Context, ids []uuid.UUID) ([]domain.File, error) {
	files := make([]domain.File, 0, len(ids))
	for _, id := range ids {
		file, err := s.files.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		// A file the client has not reported uploading may have no object
		// behind it at all, so it must not reach a worker.
		if !file.IsUsable() {
			return nil, fmt.Errorf("%w: file %s is %s, not ready", domain.ErrInvalidInput, file.ID, file.Status)
		}
		if file.Kind != contracts.KindDocument && file.Kind != contracts.KindImage {
			return nil, fmt.Errorf("%w: file %s is not a document or an image", domain.ErrInvalidInput, file.ID)
		}
		files = append(files, file)
	}
	return files, nil
}

func inputsOf(files []domain.File) []contracts.Input {
	out := make([]contracts.Input, len(files))
	for i, f := range files {
		out[i] = contracts.Input{Key: f.StorageKey, Name: f.OriginalName, Mime: f.MimeType}
	}
	return out
}
