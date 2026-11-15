package service

import (
	"errors"
	"testing"

	"github.com/alikilicw/filecosystem-be/internal/contracts"
	"github.com/alikilicw/filecosystem-be/internal/domain"
)

func TestPipelineAcceptsEditorSteps(t *testing.T) {
	in := contracts.ImageParams{
		Format:      "jpg",
		Quality:     80,
		TargetBytes: 200_000,
		Steps: []contracts.ImageStep{
			{Op: contracts.StepCrop, X: 10, Y: 20, Width: 300, Height: 200, Degrees: 5}, // stray field
			{Op: contracts.StepRotate, Degrees: -12.5, Expand: true, Fill: "white"},
			{Op: contracts.StepFlip, Axis: "vertical"},
			{Op: contracts.StepPixelate, Block: 16},
			{Op: contracts.StepPixelate, Block: 8, X: 5, Y: 5, Width: 50, Height: 50},
			{Op: contracts.StepGrayscale, Mode: "bw", Threshold: 128},
			{Op: contracts.StepResize, Width: 800, Height: 600},
		},
	}
	out, err := normalizeParams(contracts.OpPipeline, in, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if out.Format != contracts.FormatJPEG || out.Quality != 80 || out.TargetBytes != 200_000 {
		t.Fatalf("export = %s q%d target %d", out.Format, out.Quality, out.TargetBytes)
	}
	if len(out.Steps) != len(in.Steps) {
		t.Fatalf("kept %d steps, want %d", len(out.Steps), len(in.Steps))
	}
	if out.Steps[0].Degrees != 0 {
		t.Fatal("a crop step kept a rotate field")
	}
}

func TestPipelineFallsBackToTheSourceFormat(t *testing.T) {
	out, err := normalizeParams(contracts.OpPipeline, contracts.ImageParams{}, "image/webp")
	if err != nil {
		t.Fatal(err)
	}
	if out.Format != contracts.FormatWebP {
		t.Fatalf("format = %q, want webp", out.Format)
	}
}

func TestPipelineRejectsBadSteps(t *testing.T) {
	cases := map[string]contracts.ImageStep{
		"resize without height": {Op: contracts.StepResize, Width: 10},
		"resize too large":      {Op: contracts.StepResize, Width: 20000, Height: 10},
		"crop negative":         {Op: contracts.StepCrop, X: -1, Width: 10, Height: 10},
		"crop empty":            {Op: contracts.StepCrop, Width: 0, Height: 10},
		"rotate too far":        {Op: contracts.StepRotate, Degrees: 720},
		"rotate bad fill":       {Op: contracts.StepRotate, Degrees: 10, Fill: "red"},
		"flip no axis":          {Op: contracts.StepFlip},
		"pixelate tiny block":   {Op: contracts.StepPixelate, Block: 1},
		"grayscale bad mode":    {Op: contracts.StepGrayscale, Mode: "sepia"},
		"bw no threshold":       {Op: contracts.StepGrayscale, Mode: "bw"},
		"unknown":               {Op: "blur"},
	}
	for name, step := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := normalizeParams(contracts.OpPipeline,
				contracts.ImageParams{Format: "png", Steps: []contracts.ImageStep{step}}, "image/png")
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestPipelineLimits(t *testing.T) {
	steps := make([]contracts.ImageStep, contracts.MaxPipelineSteps+1)
	for i := range steps {
		steps[i] = contracts.ImageStep{Op: contracts.StepFlip, Axis: "horizontal"}
	}
	if _, err := normalizeParams(contracts.OpPipeline,
		contracts.ImageParams{Format: "png", Steps: steps}, "image/png"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("too many steps: err = %v", err)
	}
	if _, err := normalizeParams(contracts.OpPipeline,
		contracts.ImageParams{Format: "png", TargetBytes: 10}, "image/png"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("tiny target: err = %v", err)
	}
	if _, err := normalizeParams(contracts.OpPipeline,
		contracts.ImageParams{Format: "gif"}, "image/png"); !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("gif output: err = %v", err)
	}
}
