package service

import (
	"fmt"
	"math"
	"slices"

	"github.com/alikilicw/filecosystem-be/internal/contracts"
	"github.com/alikilicw/filecosystem-be/internal/domain"
)

// Limits for edits. Coordinates are only bounded loosely: the worker clamps
// every area to the image anyway.
const (
	maxSide          = 16384
	maxCoordinate    = 1 << 16
	maxPixelateBlock = 1024
	minTargetBytes   = 1 << 10
	maxTargetBytes   = 1 << 30
	// convertQuality is the quality a lossy format starts at when the user
	// converts to it: high, so the conversion changes as little as it can.
	convertQuality = 90
)

// normalizeStep checks one step and keeps only the fields that apply to its
// op, so a bad request fails here with a clear message instead of in a worker,
// and stored versions carry nothing the worker would ignore.
func normalizeStep(s contracts.ImageStep) (contracts.ImageStep, error) {
	out := contracts.ImageStep{Op: s.Op}

	switch s.Op {
	case contracts.StepResize:
		if s.Width <= 0 || s.Height <= 0 {
			return out, fmt.Errorf("width and height are required")
		}
		if s.Width > maxSide || s.Height > maxSide {
			return out, fmt.Errorf("sides may not exceed %d px", maxSide)
		}
		out.Width, out.Height = s.Width, s.Height

	case contracts.StepCrop:
		if err := checkArea(s); err != nil {
			return out, err
		}
		out.X, out.Y, out.Width, out.Height = s.X, s.Y, s.Width, s.Height

	case contracts.StepRotate:
		if math.IsNaN(s.Degrees) || math.IsInf(s.Degrees, 0) || math.Abs(s.Degrees) > 360 {
			return out, fmt.Errorf("degrees must be between -360 and 360")
		}
		if s.Fill != "" && s.Fill != "white" {
			return out, fmt.Errorf("fill must be empty or \"white\"")
		}
		out.Degrees, out.Expand, out.Fill = s.Degrees, s.Expand, s.Fill

	case contracts.StepFlip:
		if s.Axis != "horizontal" && s.Axis != "vertical" {
			return out, fmt.Errorf("axis must be \"horizontal\" or \"vertical\"")
		}
		out.Axis = s.Axis

	case contracts.StepPixelate:
		if s.Block < 2 || s.Block > maxPixelateBlock {
			return out, fmt.Errorf("block must be between 2 and %d", maxPixelateBlock)
		}
		out.Block = s.Block
		// A zero width means the whole image; otherwise it is an area.
		if s.Width != 0 || s.Height != 0 {
			if err := checkArea(s); err != nil {
				return out, err
			}
			out.X, out.Y, out.Width, out.Height = s.X, s.Y, s.Width, s.Height
		}

	case contracts.StepGrayscale:
		switch s.Mode {
		case "gray":
		case "bw":
			if s.Threshold < 1 || s.Threshold > 254 {
				return out, fmt.Errorf("threshold must be between 1 and 254")
			}
			out.Threshold = s.Threshold
		default:
			return out, fmt.Errorf("mode must be \"gray\" or \"bw\"")
		}
		out.Mode = s.Mode

	default:
		return out, fmt.Errorf("unknown step")
	}
	return out, nil
}

func checkArea(s contracts.ImageStep) error {
	if s.X < 0 || s.Y < 0 || s.X > maxCoordinate || s.Y > maxCoordinate {
		return fmt.Errorf("x and y must be between 0 and %d", maxCoordinate)
	}
	if s.Width <= 0 || s.Height <= 0 || s.Width > maxCoordinate || s.Height > maxCoordinate {
		return fmt.Errorf("width and height must be between 1 and %d", maxCoordinate)
	}
	return nil
}

// EncodeChange is what the user asked to change about how the result is
// written. A nil field keeps the parent version's setting.
type EncodeChange struct {
	Format       *string `json:"format,omitempty"`
	Quality      *int    `json:"quality,omitempty"`
	Lossless     *bool   `json:"lossless,omitempty"`
	Palette      *bool   `json:"palette,omitempty"`
	Progressive  *bool   `json:"progressive,omitempty"`
	Compression  *string `json:"compression,omitempty"`
	KeepMetadata *bool   `json:"keep_metadata,omitempty"`
	TargetBytes  *int64  `json:"target_bytes,omitempty"`
}

// mergeEncode works out a new version's settings: the parent's, as read off
// the original or chosen earlier, with the user's changes on top. A size
// target applies to one version only, so it is never inherited; the quality
// it landed on is.
func mergeEncode(parent contracts.Encode, c *EncodeChange) (contracts.Encode, error) {
	out := parent
	out.TargetBytes = 0
	if c == nil {
		return out, nil
	}

	if c.Format != nil {
		format := contracts.CanonicalFormat(*c.Format)
		if format == "" {
			return out, fmt.Errorf("%w: unsupported target format %q", domain.ErrUnsupported, *c.Format)
		}
		if format != parent.Format {
			// The parent's settings belong to another format; start from the
			// new format's own defaults.
			out = contracts.Encode{Format: format, KeepMetadata: parent.KeepMetadata}
			if contracts.HasQuality(format) {
				out.Quality = convertQuality
			}
			if format == contracts.FormatTIFF {
				out.Compression = "lzw"
			}
		}
	}
	f := out.Format

	if c.Lossless != nil {
		if f != contracts.FormatWebP && f != contracts.FormatAVIF && f != contracts.FormatHEIC {
			return out, fmt.Errorf("%w: %s has no lossless mode", domain.ErrInvalidInput, f)
		}
		out.Lossless = *c.Lossless
		if !out.Lossless && out.Quality == 0 {
			out.Quality = convertQuality
		}
	}
	if c.Palette != nil {
		if f != contracts.FormatPNG {
			return out, fmt.Errorf("%w: only a PNG can be written with a palette", domain.ErrInvalidInput)
		}
		out.Palette = *c.Palette
	}
	if c.Quality != nil {
		q := *c.Quality
		if q < 1 || q > 100 {
			return out, fmt.Errorf("%w: quality must be between 1 and 100", domain.ErrInvalidInput)
		}
		if !contracts.HasQuality(f) && !(f == contracts.FormatPNG && out.Palette) {
			return out, fmt.Errorf("%w: %s has no quality setting", domain.ErrInvalidInput, f)
		}
		out.Quality = q
		// A quality only means something for a lossy encode.
		if c.Lossless == nil {
			out.Lossless = false
		}
	}
	if c.Progressive != nil {
		if f != contracts.FormatJPEG && f != contracts.FormatPNG {
			return out, fmt.Errorf("%w: only JPEG and PNG can be progressive", domain.ErrInvalidInput)
		}
		out.Progressive = *c.Progressive
	}
	if c.Compression != nil {
		if f != contracts.FormatTIFF {
			return out, fmt.Errorf("%w: compression applies to TIFF only", domain.ErrInvalidInput)
		}
		if !slices.Contains(contracts.TIFFCompressions, *c.Compression) {
			return out, fmt.Errorf("%w: unknown TIFF compression %q", domain.ErrInvalidInput, *c.Compression)
		}
		out.Compression = *c.Compression
	}
	if c.KeepMetadata != nil {
		out.KeepMetadata = *c.KeepMetadata
	}
	if c.TargetBytes != nil && *c.TargetBytes != 0 {
		t := *c.TargetBytes
		if t < minTargetBytes || t > maxTargetBytes {
			return out, fmt.Errorf("%w: target_bytes must be between %d and %d", domain.ErrInvalidInput, minTargetBytes, int64(maxTargetBytes))
		}
		if !contracts.HasQuality(f) && f != contracts.FormatPNG {
			return out, fmt.Errorf("%w: %s cannot be compressed to a size", domain.ErrInvalidInput, f)
		}
		out.TargetBytes = t
		out.Lossless = false
	}
	return out, nil
}
