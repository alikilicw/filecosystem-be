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
	"image/tiff": {KindImage, ".tiff"},
	"image/avif": {KindImage, ".avif"},
	"image/heic": {KindImage, ".heic"},
	"image/heif": {KindImage, ".heif"},
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

// Operation is what a job asks the worker to do.
type Operation string

const (
	// OpSource opens an upload: the worker reads the original, works out how
	// it was encoded, and stores the lossless intermediate and the preview
	// every later version starts from. Its result is the original itself.
	OpSource Operation = "source"
	// OpEdit makes one version from its parent's intermediate: it applies at
	// most one step, then encodes the result with the version's settings.
	OpEdit Operation = "edit"
)

// Status is the lifecycle state of a job.
type Status string

const (
	StatusQueued     Status = "queued"
	StatusProcessing Status = "processing"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
)

// StepOp is one edit.
type StepOp string

const (
	StepResize    StepOp = "resize"
	StepCrop      StepOp = "crop"
	StepRotate    StepOp = "rotate"
	StepFlip      StepOp = "flip"
	StepPixelate  StepOp = "pixelate"
	StepGrayscale StepOp = "grayscale"
)

// ImageStep is one edit, in the pixels of the version it is applied to (one
// frame, for an animation: every frame gets the same edit). Which fields
// apply depends on Op:
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
	FormatGIF  = "gif"
	FormatTIFF = "tiff"
	FormatAVIF = "avif"
	FormatHEIC = "heic"
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
	case FormatGIF:
		return ".gif", "image/gif", true
	case FormatTIFF, "tif":
		return ".tiff", "image/tiff", true
	case FormatAVIF:
		return ".avif", "image/avif", true
	case FormatHEIC, "heif":
		return ".heic", "image/heic", true
	}
	return "", "", false
}

// CanonicalFormat folds an alias ("jpg", "tif", "heif") to the name used
// everywhere else, or returns "" for a format no worker can write.
func CanonicalFormat(format string) string {
	_, mime, ok := FormatInfo(format)
	if !ok {
		return ""
	}
	return FormatFromMime(mime)
}

// FormatFromMime is the inverse of FormatInfo, used when a job keeps the
// source format. It returns "" for inputs no worker can write (BMP).
func FormatFromMime(mime string) string {
	switch normalizeMime(mime) {
	case "image/jpeg":
		return FormatJPEG
	case "image/png":
		return FormatPNG
	case "image/webp":
		return FormatWebP
	case "image/gif":
		return FormatGIF
	case "image/tiff":
		return FormatTIFF
	case "image/avif":
		return FormatAVIF
	case "image/heic", "image/heif":
		return FormatHEIC
	}
	return ""
}

// HasQuality reports whether a format has a lossy quality setting.
func HasQuality(format string) bool {
	switch format {
	case FormatJPEG, FormatWebP, FormatAVIF, FormatHEIC:
		return true
	}
	return false
}

// TIFF compressions the workers can write.
var TIFFCompressions = []string{"none", "lzw", "deflate", "packbits", "jpeg", "zstd", "webp"}

// Encode is how a version is written. The source job reads it off the
// original, and every later version inherits its parent's unless the user
// changes it, so an edit never re-encodes a file any differently from how it
// came in. Fields that do not apply to Format are ignored.
type Encode struct {
	Format string `json:"format" bson:"format"`
	// Quality is 1–100 for lossy encodes, and the quantiser's quality for a
	// PNG written with Palette.
	Quality int `json:"quality,omitempty" bson:"quality,omitempty"`
	// Lossless is WebP's VP8L and AVIF/HEIC's lossless mode.
	Lossless bool `json:"lossless,omitempty" bson:"lossless,omitempty"`
	// Subsample is the JPEG chroma subsampling: "on" (4:2:0) or "off" (4:4:4).
	Subsample string `json:"subsample,omitempty" bson:"subsample,omitempty"`
	// Progressive is a progressive JPEG or an interlaced PNG.
	Progressive bool `json:"progressive,omitempty" bson:"progressive,omitempty"`
	// Palette quantises a PNG to at most 256 colours. It is lossy, so it is
	// only ever on because the user asked to compress.
	Palette bool `json:"palette,omitempty" bson:"palette,omitempty"`
	// Compression is the TIFF compression, one of TIFFCompressions.
	Compression string `json:"compression,omitempty" bson:"compression,omitempty"`
	// KeepMetadata carries EXIF, XMP and the ICC profile over.
	KeepMetadata bool `json:"keep_metadata" bson:"keep_metadata"`
	// TargetBytes, when set, makes the encoder search for the highest quality
	// that fits under it. The quality it lands on is reported back.
	TargetBytes int64 `json:"target_bytes,omitempty" bson:"target_bytes,omitempty"`
}

// Animation describes a multi-frame image. The intermediate holds the frames
// stacked top to bottom, and a PNG has nowhere to keep the timing, so it
// travels with the version instead.
type Animation struct {
	Frames int `json:"frames" bson:"frames"`
	// FrameHeight is one frame's height in the stacked intermediate.
	FrameHeight int `json:"frame_height" bson:"frame_height"`
	// Delays is each frame's duration in milliseconds.
	Delays []int `json:"delays,omitempty" bson:"delays,omitempty"`
	// Loop is how many times it plays; 0 is forever.
	Loop int `json:"loop" bson:"loop"`
}

// MaxVersions bounds how many edits one session can hold.
const MaxVersions = 100

// JobMessage is published by the API and consumed by a worker.
type JobMessage struct {
	JobID      string    `json:"job_id"`
	FileID     string    `json:"file_id"`
	SessionID  string    `json:"session_id"`
	Seq        int       `json:"seq"`
	Kind       Kind      `json:"kind"`
	Operation  Operation `json:"operation"`
	SourceKey  string    `json:"source_key"`
	SourceName string    `json:"source_name"`
	SourceMime string    `json:"source_mime"`
	// InputKey is what the job starts from: the original for OpSource, the
	// parent version's intermediate for OpEdit.
	InputKey string `json:"input_key"`
	// Animation is the input's frame layout, when it has more than one.
	Animation *Animation `json:"animation,omitempty"`
	// Step is the one edit an OpEdit job applies; nil when only the encoding
	// changes, in which case the parent's intermediate is reused as is.
	Step *ImageStep `json:"step,omitempty"`
	// Encode is how an OpEdit job writes its result. OpSource ignores it.
	Encode *Encode `json:"encode,omitempty"`
	// OutputPrefix is where the job stores what it makes, ending in "/".
	OutputPrefix string    `json:"output_prefix"`
	CreatedAt    time.Time `json:"created_at"`
}

// JobResult is published by a worker once processing finishes and consumed by
// the API, which owns the database.
type JobResult struct {
	JobID           string `json:"job_id"`
	Status          Status `json:"status"`
	ResultKey       string `json:"result_key,omitempty"`
	ResultName      string `json:"result_name,omitempty"`
	ResultSize      int64  `json:"result_size,omitempty"`
	ResultMime      string `json:"result_mime,omitempty"`
	IntermediateKey string `json:"intermediate_key,omitempty"`
	PreviewKey      string `json:"preview_key,omitempty"`
	// Width and Height are one frame's size.
	Width     int        `json:"width,omitempty"`
	Height    int        `json:"height,omitempty"`
	Animation *Animation `json:"animation,omitempty"`
	// Encode is how the result was actually written: for OpSource, the
	// settings read off the original; for OpEdit, the requested ones with the
	// quality a size target landed on.
	Encode     *Encode   `json:"encode,omitempty"`
	Error      string    `json:"error,omitempty"`
	FinishedAt time.Time `json:"finished_at"`
}
