package service

import (
	"errors"
	"testing"

	"github.com/alikilicw/filecosystem-be/internal/contracts"
	"github.com/alikilicw/filecosystem-be/internal/domain"
)

func ptr[T any](v T) *T { return &v }

func TestStepKeepsOnlyItsFields(t *testing.T) {
	out, err := normalizeStep(contracts.ImageStep{Op: contracts.StepCrop, X: 10, Y: 20, Width: 300, Height: 200, Degrees: 5})
	if err != nil {
		t.Fatal(err)
	}
	if out.Degrees != 0 || out.Width != 300 {
		t.Fatalf("crop = %+v", out)
	}
	for _, s := range []contracts.ImageStep{
		{Op: contracts.StepRotate, Degrees: -12.5, Expand: true, Fill: "white"},
		{Op: contracts.StepFlip, Axis: "vertical"},
		{Op: contracts.StepPixelate, Block: 16},
		{Op: contracts.StepPixelate, Block: 8, X: 5, Y: 5, Width: 50, Height: 50},
		{Op: contracts.StepGrayscale, Mode: "bw", Threshold: 128},
		{Op: contracts.StepResize, Width: 800, Height: 600},
	} {
		if _, err := normalizeStep(s); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
}

func TestStepRejectsBadValues(t *testing.T) {
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
		if _, err := normalizeStep(step); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

var jpegSource = contracts.Encode{
	Format: contracts.FormatJPEG, Quality: 87, Subsample: "off", Progressive: true, KeepMetadata: true,
}

func TestEncodeIsInheritedUnchanged(t *testing.T) {
	parent := jpegSource
	parent.TargetBytes = 50_000
	out, err := mergeEncode(parent, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := jpegSource
	if out != want {
		t.Fatalf("inherited %+v, want %+v (target cleared, rest kept)", out, want)
	}
}

func TestEncodeChangesOnlyWhatWasAsked(t *testing.T) {
	out, err := mergeEncode(jpegSource, &EncodeChange{Quality: ptr(60)})
	if err != nil {
		t.Fatal(err)
	}
	if out.Quality != 60 || out.Subsample != "off" || !out.Progressive || !out.KeepMetadata {
		t.Fatalf("got %+v", out)
	}

	out, err = mergeEncode(jpegSource, &EncodeChange{KeepMetadata: ptr(false)})
	if err != nil || out.KeepMetadata || out.Quality != 87 {
		t.Fatalf("got %+v, %v", out, err)
	}
}

func TestConvertStartsFromTheFormatsDefaults(t *testing.T) {
	out, err := mergeEncode(jpegSource, &EncodeChange{Format: ptr("webp")})
	if err != nil {
		t.Fatal(err)
	}
	want := contracts.Encode{Format: contracts.FormatWebP, Quality: convertQuality, KeepMetadata: true}
	if out != want {
		t.Fatalf("got %+v, want %+v", out, want)
	}

	out, _ = mergeEncode(jpegSource, &EncodeChange{Format: ptr("tif")})
	if out.Format != contracts.FormatTIFF || out.Compression != "lzw" {
		t.Fatalf("tiff = %+v", out)
	}
	out, _ = mergeEncode(jpegSource, &EncodeChange{Format: ptr("jpg")})
	if out != jpegSource {
		t.Fatalf("same format reset the settings: %+v", out)
	}
}

func TestCompressToASize(t *testing.T) {
	png := contracts.Encode{Format: contracts.FormatPNG, KeepMetadata: true}
	out, err := mergeEncode(png, &EncodeChange{TargetBytes: ptr(int64(200_000))})
	if err != nil || out.TargetBytes != 200_000 {
		t.Fatalf("png target: %+v, %v", out, err)
	}
	webp := contracts.Encode{Format: contracts.FormatWebP, Lossless: true}
	out, err = mergeEncode(webp, &EncodeChange{TargetBytes: ptr(int64(200_000))})
	if err != nil || out.Lossless {
		t.Fatalf("lossless webp target: %+v, %v", out, err)
	}
}

func TestEncodeRejectsWhatTheFormatCannotDo(t *testing.T) {
	cases := map[string]struct {
		parent contracts.Encode
		change EncodeChange
		want   error
	}{
		"unknown format":       {jpegSource, EncodeChange{Format: ptr("bmp")}, domain.ErrUnsupported},
		"quality out of range": {jpegSource, EncodeChange{Quality: ptr(0)}, domain.ErrInvalidInput},
		"quality on png":       {contracts.Encode{Format: contracts.FormatPNG}, EncodeChange{Quality: ptr(50)}, domain.ErrInvalidInput},
		"lossless jpeg":        {jpegSource, EncodeChange{Lossless: ptr(true)}, domain.ErrInvalidInput},
		"palette jpeg":         {jpegSource, EncodeChange{Palette: ptr(true)}, domain.ErrInvalidInput},
		"tiny target":          {jpegSource, EncodeChange{TargetBytes: ptr(int64(10))}, domain.ErrInvalidInput},
		"gif target":           {contracts.Encode{Format: contracts.FormatGIF}, EncodeChange{TargetBytes: ptr(int64(50_000))}, domain.ErrInvalidInput},
		"bad tiff compression": {contracts.Encode{Format: contracts.FormatTIFF}, EncodeChange{Compression: ptr("lzma")}, domain.ErrInvalidInput},
	}
	for name, c := range cases {
		if _, err := mergeEncode(c.parent, &c.change); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
}
