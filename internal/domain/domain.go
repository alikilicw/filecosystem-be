package domain

import (
	"errors"
	"time"

	"github.com/alikilicw/filecosystem-be/internal/contracts"
	"github.com/google/uuid"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrUnsupported  = errors.New("unsupported")
	ErrInvalidInput = errors.New("invalid input")
	// ErrBusy means a session already has an operation running; only one runs
	// at a time.
	ErrBusy = errors.New("busy")
)

// File is an upload and everything known about it. Every field but Status and
// the timestamps is what the client claimed at ticket time; nothing inspects the
// stored object, so none of it is verified.
type File struct {
	ID           uuid.UUID            `json:"id"`
	OriginalName string               `json:"original_name"`
	StorageKey   string               `json:"-"`
	MimeType     string               `json:"mime_type"`
	SizeBytes    int64                `json:"size_bytes"`
	Kind         contracts.Kind       `json:"kind"`
	Status       contracts.FileStatus `json:"status"`
	CreatedAt    time.Time            `json:"created_at"`
	UpdatedAt    time.Time            `json:"updated_at"`
	ExpiresAt    time.Time            `json:"expires_at"`
}

// IsUsable reports whether the file may be used as a source for a job. A file
// nobody has reported uploading has no object behind it yet.
func (f File) IsUsable() bool { return f.Status == contracts.FileStatusReady }

// Session is one editing session on an uploaded image or document. Its
// versions are jobs: version 0 opens the upload, and every later one is made
// from its parent's intermediate. Head is the version the user is looking at;
// undo and reset only move it.
type Session struct {
	ID   uuid.UUID
	Kind contracts.Kind
	// FileID is the upload the session was opened from; a document opened
	// from several files keeps the first, and the session lives as long as it.
	FileID   uuid.UUID
	SourceID uuid.UUID
	HeadID   uuid.UUID
	// NextSeq is the number the next version gets.
	NextSeq int
	// LockJobID is the job running now, or uuid.Nil. The lock lapses at
	// LockExpiresAt so a lost worker cannot freeze a session for good.
	LockJobID     uuid.UUID
	LockExpiresAt time.Time
	// LastError is why the most recent operation failed, cleared by the next.
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Locked reports whether an operation is running at now.
func (s Session) Locked(now time.Time) bool {
	return s.LockJobID != uuid.Nil && now.Before(s.LockExpiresAt)
}

// Job is one version of a session.
type Job struct {
	ID        uuid.UUID
	FileID    uuid.UUID
	SessionID uuid.UUID
	// ParentID is the version this one was made from; uuid.Nil for version 0.
	ParentID  uuid.UUID
	Seq       int
	Kind      contracts.Kind
	Operation contracts.Operation
	Status    contracts.Status
	Step      *contracts.ImageStep
	DocStep   *contracts.DocumentStep
	// Pages are a document version's pages, once the worker reports them.
	Pages []contracts.PageInfo
	// Encode is how the result is written: requested while queued, actual once
	// the worker reports back.
	Encode          *contracts.Encode
	Animation       *contracts.Animation
	IntermediateKey string
	PreviewKey      string
	ResultKey       string
	ResultName      string
	ResultSize      int64
	ResultMime      string
	Width           int
	Height          int
	Error           string
	// ErrorCode is the worker's contracts.Fail* code for a failure the user
	// can act on.
	ErrorCode  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	FinishedAt *time.Time
}

func (j Job) IsTerminal() bool {
	return j.Status == contracts.StatusSucceeded || j.Status == contracts.StatusFailed
}
