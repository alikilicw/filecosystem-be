// Package contracts holds the message schema shared between the API and the
// workers. Both repositories keep a byte-identical copy of this file; changing
// one without the other breaks the queue protocol.
package contracts

import (
	"strings"
	"time"
)

const (
	ExchangeJobs   = "filecosystem.jobs"
	ExchangeEvents = "filecosystem.events"

	QueueImageJobs  = "image.jobs"
	QueueJobResults = "job.results"

	RoutingKeyImage     = "job.image"
	RoutingKeyJobResult = "job.result"
)

// Kind is the file family a job belongs to. Only image is implemented today;
// the others are reserved so the routing scheme does not have to change later.
type Kind string

const (
	KindImage    Kind = "image"
	KindVideo    Kind = "video"
	KindAudio    Kind = "audio"
	KindDocument Kind = "document"
)

// Operation is the transformation requested for a job.
type Operation string

const (
	OpConvert  Operation = "convert"
	OpResize   Operation = "resize"
	OpCompress Operation = "compress"
)

// Status is the lifecycle state of a job.
type Status string

const (
	StatusQueued     Status = "queued"
	StatusProcessing Status = "processing"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
)

// Fit controls how a resize handles a mismatched aspect ratio.
type Fit string

const (
	FitContain Fit = "contain" // fit inside the box, keep aspect ratio
	FitCover   Fit = "cover"   // fill the box, crop the overflow
	FitFill    Fit = "fill"    // stretch to the exact box
)

// ImageParams carries the tunables for every image operation. Fields not
// relevant to the requested operation are left at their zero value.
type ImageParams struct {
	Format        string `json:"format,omitempty" bson:"format,omitempty"`
	Quality       int    `json:"quality,omitempty" bson:"quality,omitempty"`
	Width         int    `json:"width,omitempty" bson:"width,omitempty"`
	Height        int    `json:"height,omitempty" bson:"height,omitempty"`
	Fit           Fit    `json:"fit,omitempty" bson:"fit,omitempty"`
	StripMetadata bool   `json:"strip_metadata,omitempty" bson:"strip_metadata,omitempty"`
}

// Output formats the workers can encode. Keeping the list here means the API
// can reject an impossible request before it ever reaches the queue.
const (
	FormatJPEG = "jpeg"
	FormatPNG  = "png"
	FormatWebP = "webp"
)

// FormatInfo resolves a target format name to its canonical extension and MIME
// type. It reports false for formats the workers cannot encode.
func FormatInfo(format string) (ext, mime string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case FormatJPEG, "jpg":
		return ".jpg", "image/jpeg", true
	case FormatPNG:
		return ".png", "image/png", true
	case FormatWebP:
		return ".webp", "image/webp", true
	}
	return "", "", false
}

// FormatFromMime is the inverse of FormatInfo, used when an operation keeps the
// source format instead of converting it. It returns "" for unencodable inputs.
func FormatFromMime(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg":
		return FormatJPEG
	case "image/png":
		return FormatPNG
	case "image/webp":
		return FormatWebP
	}
	return ""
}

// JobMessage is published by the API and consumed by a worker.
type JobMessage struct {
	JobID      string      `json:"job_id"`
	FileID     string      `json:"file_id"`
	Kind       Kind        `json:"kind"`
	Operation  Operation   `json:"operation"`
	SourceKey  string      `json:"source_key"`
	SourceName string      `json:"source_name"`
	SourceMime string      `json:"source_mime"`
	Params     ImageParams `json:"params"`
	CreatedAt  time.Time   `json:"created_at"`
}

// JobResult is published by a worker once processing finishes and consumed by
// the API, which owns the database.
type JobResult struct {
	JobID      string    `json:"job_id"`
	Status     Status    `json:"status"`
	ResultKey  string    `json:"result_key,omitempty"`
	ResultName string    `json:"result_name,omitempty"`
	ResultSize int64     `json:"result_size,omitempty"`
	ResultMime string    `json:"result_mime,omitempty"`
	Width      int       `json:"width,omitempty"`
	Height     int       `json:"height,omitempty"`
	Error      string    `json:"error,omitempty"`
	FinishedAt time.Time `json:"finished_at"`
}
