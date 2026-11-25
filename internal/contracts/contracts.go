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

	QueueImageJobs    = "image.jobs"
	QueueDocumentJobs = "document.jobs"
	QueueJobResults   = "job.results"

	RoutingKeyImage     = "job.image"
	RoutingKeyDocument  = "job.document"
	RoutingKeyJobResult = "job.result"
)

// RoutingKey is where a job of this kind is published, or "" for a family no
// worker consumes yet.
func RoutingKey(kind Kind) string {
	switch kind {
	case KindImage:
		return RoutingKeyImage
	case KindDocument:
		return RoutingKeyDocument
	}
	return ""
}

// Kind is the file family a job belongs to. Image and document are
// implemented; the others are reserved so the routing scheme does not have to
// change later.
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

	"application/pdf": {KindDocument, ".pdf"},
}

// officeMimes are the office documents a worker converts to PDF before
// anything else, so to the editor they are PDFs like any other. They are
// documents to the rest of the platform.
var officeMimes = map[string]string{
	"application/msword": ".doc",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": ".docx",
	"application/vnd.oasis.opendocument.text":                                 ".odt",
	"application/rtf":          ".rtf",
	"text/rtf":                 ".rtf",
	"application/vnd.ms-excel": ".xls",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         ".xlsx",
	"application/vnd.oasis.opendocument.spreadsheet":                            ".ods",
	"application/vnd.ms-powerpoint":                                             ".ppt",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": ".pptx",
	"application/vnd.oasis.opendocument.presentation":                           ".odp",
}

func init() {
	for mime, ext := range officeMimes {
		inputMimes[mime] = inputMime{KindDocument, ext}
	}
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

// IsOffice reports whether a content type is an office document a worker
// converts to PDF.
func IsOffice(mime string) bool {
	_, ok := officeMimes[normalizeMime(mime)]
	return ok
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

// Limits for document sessions. A document is opened from up to
// MaxDocumentInputs files, and no version may have more than MaxDocumentPages
// pages: every page gets its own preview, so the page count is what a job
// costs.
const (
	MaxDocumentInputs = 20
	MaxDocumentPages  = 300
)

// DocStepOp is one document edit.
type DocStepOp string

const (
	// DocOrganize rebuilds the page list: it reorders, drops and rotates
	// pages in one go, which is what deleting, extracting, reordering and
	// rotating all are.
	DocOrganize DocStepOp = "organize"
	// DocInsert puts the pages of other PDFs into the document.
	DocInsert DocStepOp = "insert"
	// DocCompress makes the PDF smaller: it drops what is unused or repeated
	// and re-encodes large images at a lower resolution and quality. The
	// pages stay the same pages, so their previews are kept.
	DocCompress DocStepOp = "compress"
	// DocPageNumbers draws a number onto every page, or every page but the
	// first. The content changes, so every page is rendered again.
	DocPageNumbers DocStepOp = "page_numbers"
	// DocWatermark draws a see-through line of text across every page.
	DocWatermark DocStepOp = "watermark"

	// The steps below export the document instead of changing it: the
	// version's download is what they make, while its pages and the PDF the
	// next edit starts from stay the parent's.

	// DocSplit cuts the document into several PDFs, delivered as a ZIP.
	DocSplit DocStepOp = "split"
	// DocImages draws pages as images: one image, or a ZIP of several.
	DocImages DocStepOp = "images"
	// DocProtect encrypts the document with a password.
	DocProtect DocStepOp = "protect"
)

// Exports reports whether a step exports the document rather than editing it.
func (op DocStepOp) Exports() bool {
	return op == DocSplit || op == DocImages || op == DocProtect
}

// Limits for exporting pages as images. A page is drawn at DPI, but never
// with a side longer than MaxImageSide pixels, so a poster-sized page stays
// a sane image.
const (
	MinImageDPI  = 72
	MaxImageDPI  = 300
	MaxImageSide = 6000
	// MaxPasswordBytes is the longest password a PDF can carry (AES-256).
	MaxPasswordBytes = 127
)

// How hard a compress step works on the images of a document.
const (
	CompressLight       = "light"
	CompressRecommended = "recommended"
	CompressStrong      = "strong"
)

// Page number and watermark settings. Sizes are steps, not points: the
// worker picks the font size, since a page number and a watermark mean very
// different things by "large".
const (
	MaxStampText = 80
	SizeSmall    = "small"
	SizeMedium   = "medium"
	SizeLarge    = "large"
	MinOpacity   = 0.1
	MaxStart     = 100000
)

// NumberPositions are where a page number may go: top or bottom, then left,
// centre or right.
var NumberPositions = []string{"tl", "tc", "tr", "bl", "bc", "br"}

// PageRef is one page of an organize step: which page of the parent it is
// (1-based) and how far to turn it clockwise on top of its current rotation.
type PageRef struct {
	Page   int `json:"page" bson:"page"`
	Rotate int `json:"rotate,omitempty" bson:"rotate,omitempty"`
}

// DocumentStep is one edit to a document. Which fields apply depends on Op:
//
//	organize  Pages: the new page list, in order; a parent page left out is
//	          dropped
//	insert    Files: the uploads whose pages go in, in order; At: how many of
//	          the parent's pages come before them (0 puts them first)
//	split     Cuts: the pages each part but the last ends on, ascending
//	images    Select: the pages to draw (1-based, ascending), all when empty;
//	          Format "jpeg" or "png"; DPI between MinImageDPI and MaxImageDPI
//	protect   Password: the password the PDF will open with
//	compress  Level: CompressLight, CompressRecommended or CompressStrong
//	page_numbers
//	          Text: the template, with {n} for the number and {total} for
//	          the last number; Position: one of NumberPositions; Start: the
//	          first number; SkipFirst: leave the first page unnumbered;
//	          Size: SizeSmall, SizeMedium or SizeLarge
//	watermark Text; Size; Color "#rrggbb"; Opacity between MinOpacity and
//	          1; Diagonal: along the diagonal instead of level
type DocumentStep struct {
	Op     DocStepOp `json:"op" bson:"op"`
	Pages  []PageRef `json:"pages,omitempty" bson:"pages,omitempty"`
	Files  []string  `json:"files,omitempty" bson:"files,omitempty"`
	At     int       `json:"at,omitempty" bson:"at,omitempty"`
	Cuts   []int     `json:"cuts,omitempty" bson:"cuts,omitempty"`
	Select []int     `json:"select,omitempty" bson:"select,omitempty"`
	Format string    `json:"format,omitempty" bson:"format,omitempty"`
	DPI    int       `json:"dpi,omitempty" bson:"dpi,omitempty"`
	Level  string    `json:"level,omitempty" bson:"level,omitempty"`

	Text      string  `json:"text,omitempty" bson:"text,omitempty"`
	Position  string  `json:"position,omitempty" bson:"position,omitempty"`
	Start     int     `json:"start,omitempty" bson:"start,omitempty"`
	SkipFirst bool    `json:"skip_first,omitempty" bson:"skip_first,omitempty"`
	Size      string  `json:"size,omitempty" bson:"size,omitempty"`
	Color     string  `json:"color,omitempty" bson:"color,omitempty"`
	Opacity   float64 `json:"opacity,omitempty" bson:"opacity,omitempty"`
	Diagonal  bool    `json:"diagonal,omitempty" bson:"diagonal,omitempty"`
	// Password travels to the worker in the job message only. It is never
	// written to the database, and never sent back to a client.
	Password string `json:"password,omitempty" bson:"-"`
}

// PageInfo is one page of a document version. The preview is always drawn
// upright, with the page's own rotation taken out, and Rotate is how far it
// must be turned clockwise to show the page as a reader sees it. Width and
// Height are the unrotated page in PDF points.
type PageInfo struct {
	PreviewKey string  `json:"preview_key" bson:"preview_key"`
	Width      float64 `json:"width" bson:"width"`
	Height     float64 `json:"height" bson:"height"`
	Rotate     int     `json:"rotate,omitempty" bson:"rotate,omitempty"`
}

// Failure codes a worker attaches to a failed result when the cause is the
// file itself, so the user can be told what to do about it.
const (
	FailEncrypted    = "encrypted"
	FailTooManyPages = "too_many_pages"
	FailUnreadable   = "unreadable"
	// FailWrongPassword means a password was given for an encrypted PDF and
	// it does not open it.
	FailWrongPassword = "wrong_password"
	// FailConvert means an office file could not be converted to PDF.
	FailConvert = "convert_failed"
)

// Input is one stored file a job reads besides InputKey. A document may be
// opened from images as well as PDFs: each image becomes a page.
type Input struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	// Password opens the input if it is an encrypted PDF. Like a step's
	// password it is only ever in the job message.
	Password string `json:"password,omitempty"`
}

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
	// DocStep is the one edit an OpEdit document job applies.
	DocStep *DocumentStep `json:"doc_step,omitempty"`
	// Inputs are the files a document job reads: every file the document is
	// opened from for OpSource (InputKey is the first of them), the files an
	// insert step adds for OpEdit.
	Inputs []Input `json:"inputs,omitempty"`
	// Pages are the parent version's pages, for an OpEdit document job.
	Pages []PageInfo `json:"pages,omitempty"`
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
	Encode *Encode `json:"encode,omitempty"`
	// Pages are a document version's pages.
	Pages []PageInfo `json:"pages,omitempty"`
	Error string     `json:"error,omitempty"`
	// Code says why a job failed, when it is something the user can act on;
	// empty for anything else.
	Code       string    `json:"code,omitempty"`
	FinishedAt time.Time `json:"finished_at"`
}
