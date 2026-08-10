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
)

type File struct {
	ID           uuid.UUID      `json:"id"`
	OriginalName string         `json:"original_name"`
	StorageKey   string         `json:"-"`
	MimeType     string         `json:"mime_type"`
	SizeBytes    int64          `json:"size_bytes"`
	Kind         contracts.Kind `json:"kind"`
	Width        int            `json:"width,omitempty"`
	Height       int            `json:"height,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	ExpiresAt    time.Time      `json:"expires_at"`
}

type Job struct {
	ID         uuid.UUID             `json:"id"`
	FileID     uuid.UUID             `json:"file_id"`
	Kind       contracts.Kind        `json:"kind"`
	Operation  contracts.Operation   `json:"operation"`
	Status     contracts.Status      `json:"status"`
	Params     contracts.ImageParams `json:"params"`
	ResultName string                `json:"result_name,omitempty"`
	ResultKey  string                `json:"-"`
	ResultSize int64                 `json:"result_size,omitempty"`
	ResultMime string                `json:"result_mime,omitempty"`
	Width      int                   `json:"width,omitempty"`
	Height     int                   `json:"height,omitempty"`
	Error      string                `json:"error,omitempty"`
	CreatedAt  time.Time             `json:"created_at"`
	UpdatedAt  time.Time             `json:"updated_at"`
	FinishedAt *time.Time            `json:"finished_at,omitempty"`
}

func (j Job) IsTerminal() bool {
	return j.Status == contracts.StatusSucceeded || j.Status == contracts.StatusFailed
}
