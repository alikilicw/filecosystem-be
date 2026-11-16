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

// FileStatus tracks an upload through the direct-to-S3 handshake. The API hands
// out a presigned URL (pending) and the client reports that it uploaded the
// bytes (ready). Nothing inspects what actually landed in the bucket, so ready
// means the client said so and nothing more.
type FileStatus string

const (
	FileStatusPending FileStatus = "pending"
	FileStatusReady   FileStatus = "ready"
)

// inputMime describes one content type the platform accepts.
type inputMime struct {
	kind Kind
	ext  string
}

// inputMimes is the single source of truth for what may be uploaded. The API
// consults it when handing out an upload URL, taking the client at its word;
// nothing checks the bytes that actually arrive, so a mislabelled upload is only
// caught when a worker fails to decode it.
var inputMimes = map[string]inputMime{
	"image/jpeg": {KindImage, ".jpg"},
	"image/png":  {KindImage, ".png"},
	"image/webp": {KindImage, ".webp"},
	"image/gif":  {KindImage, ".gif"},
	"image/bmp":  {KindImage, ".bmp"},
}

// InputMime resolves a content type to the family that handles it and the
// extension its storage key should carry. It reports false for anything the
// platform does not accept as an upload.
func InputMime(mime string) (kind Kind, ext string, ok bool) {
	entry, ok := inputMimes[normalizeMime(mime)]
	if !ok {
		return "", "", false
	}
	return entry.kind, entry.ext, true
}

// normalizeMime drops any parameters (charset and friends) and folds case, so a
// declared "image/jpeg; charset=utf-8" matches the table.
func normalizeMime(mime string) string {
	if idx := strings.IndexByte(mime, ';'); idx >= 0 {
		mime = mime[:idx]
	}
	return strings.ToLower(strings.TrimSpace(mime))
}

// Operation is the transformation requested for a job.
type Operation string

// OpPipeline replays the image editor's edits (Params.Steps) on the original,
// at full resolution, then encodes it once. It is the only image operation:
// resizing, compressing and converting are all steps or output settings of it.
const OpPipeline Operation = "pipeline"

// Status is the lifecycle state of a job.
type Status string

const (
	StatusQueued     Status = "queued"
	StatusProcessing Status = "processing"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
)

// ImageParams carries a pipeline job's output settings and its edits.
type ImageParams struct {
	Format        string `json:"format,omitempty" bson:"format,omitempty"`
	Quality       int    `json:"quality,omitempty" bson:"quality,omitempty"`
	StripMetadata bool   `json:"strip_metadata,omitempty" bson:"strip_metadata,omitempty"`

	// Steps run in order; TargetBytes, when set, makes the encoder pick the
	// highest quality whose output fits under it.
	Steps       []ImageStep `json:"steps,omitempty" bson:"steps,omitempty"`
	TargetBytes int64       `json:"target_bytes,omitempty" bson:"target_bytes,omitempty"`
}

// StepOp is one edit in a pipeline.
type StepOp string

const (
	StepResize    StepOp = "resize"
	StepCrop      StepOp = "crop"
	StepRotate    StepOp = "rotate"
	StepFlip      StepOp = "flip"
	StepPixelate  StepOp = "pixelate"
	StepGrayscale StepOp = "grayscale"
)

// MaxPipelineSteps bounds how much work one job can ask for.
const MaxPipelineSteps = 50

// ImageStep is one edit, in the pixels of the image as it is when the step
// runs (after the steps before it). Which fields apply depends on Op:
//
//	resize     Width, Height (exact output size)
//	crop       X, Y, Width, Height
//	rotate     Degrees (clockwise); Expand grows the canvas to fit the
//	           rotated image instead of cutting the corners; Fill "white"
//	           paints the uncovered corners, otherwise they stay transparent
//	flip       Axis "horizontal" or "vertical"
//	pixelate   Block (size of one block); X, Y, Width, Height limit it to an
//	           area, and a zero Width means the whole image
//	grayscale  Mode "gray" (Rec. 601 luma) or "bw" (luma >= Threshold is white)
type ImageStep struct {
	Op        StepOp  `json:"op" bson:"op"`
	X         int     `json:"x,omitempty" bson:"x,omitempty"`
	Y         int     `json:"y,omitempty" bson:"y,omitempty"`
	Width     int     `json:"width,omitempty" bson:"width,omitempty"`
	Height    int     `json:"height,omitempty" bson:"height,omitempty"`
	Degrees   float64 `json:"degrees,omitempty" bson:"degrees,omitempty"`
	Expand    bool    `json:"expand,omitempty" bson:"expand,omitempty"`
	Fill      string  `json:"fill,omitempty" bson:"fill,omitempty"`
	Axis      string  `json:"axis,omitempty" bson:"axis,omitempty"`
	Block     int     `json:"block,omitempty" bson:"block,omitempty"`
	Mode      string  `json:"mode,omitempty" bson:"mode,omitempty"`
	Threshold int     `json:"threshold,omitempty" bson:"threshold,omitempty"`
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

// FormatFromMime is the inverse of FormatInfo, used when a job keeps the
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
