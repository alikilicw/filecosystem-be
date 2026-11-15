package service

import (
	"fmt"
	"math"

	"github.com/alikilicw/filecosystem-be/internal/contracts"
	"github.com/alikilicw/filecosystem-be/internal/domain"
)

// Limits for pipeline jobs. The resize cap matches the browser editor's canvas
// cap, so anything the editor lets someone type is accepted here. Coordinates
// are only bounded loosely: the worker clamps every area to the image anyway.
const (
	maxPipelineSide  = 16384
	maxCoordinate    = 1 << 16
	maxPixelateBlock = 1024
	minTargetBytes   = 1 << 10
	maxTargetBytes   = 1 << 30
)

// normalizePipeline checks a pipeline job from the image editor: every step
// has to be one the worker can run, with values in range, so a bad request
// fails here with a clear message instead of halfway through a worker.
func normalizePipeline(p contracts.ImageParams, fallbackFormat string) (contracts.ImageParams, error) {
	out := contracts.ImageParams{StripMetadata: p.StripMetadata}

	if len(p.Steps) > contracts.MaxPipelineSteps {
		return out, fmt.Errorf("%w: at most %d steps", domain.ErrInvalidInput, contracts.MaxPipelineSteps)
	}

	out.Format, out.Quality = resolveOutputFormat(p.Format, fallbackFormat, p.Quality)
	if out.Format == "" {
		return out, fmt.Errorf("%w: unsupported target format %q", domain.ErrUnsupported, p.Format)
	}

	if p.TargetBytes != 0 {
		if p.TargetBytes < minTargetBytes || p.TargetBytes > maxTargetBytes {
			return out, fmt.Errorf("%w: target_bytes must be between %d and %d", domain.ErrInvalidInput, minTargetBytes, int64(maxTargetBytes))
		}
		out.TargetBytes = p.TargetBytes
	}

	out.Steps = make([]contracts.ImageStep, 0, len(p.Steps))
	for i, step := range p.Steps {
		clean, err := normalizeStep(step)
		if err != nil {
			return out, fmt.Errorf("%w: step %d (%s): %v", domain.ErrInvalidInput, i+1, step.Op, err)
		}
		out.Steps = append(out.Steps, clean)
	}
	return out, nil
}

// normalizeStep keeps only the fields that apply to the step's op, so stored
// jobs carry nothing the worker would ignore.
func normalizeStep(s contracts.ImageStep) (contracts.ImageStep, error) {
	out := contracts.ImageStep{Op: s.Op}

	switch s.Op {
	case contracts.StepResize:
		if s.Width <= 0 || s.Height <= 0 {
			return out, fmt.Errorf("width and height are required")
		}
		if s.Width > maxPipelineSide || s.Height > maxPipelineSide {
			return out, fmt.Errorf("sides may not exceed %d px", maxPipelineSide)
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
