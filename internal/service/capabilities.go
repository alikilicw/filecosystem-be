package service

import "github.com/alikilicw/filecosystem-be/internal/contracts"

// OperationInfo describes a single transformation to the frontend.
type OperationInfo struct {
	Key         contracts.Operation `json:"key"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
}

// Capability describes what the platform can do for one file family. Video,
// audio and document entries are advertised as unavailable so the frontend can
// render them as upcoming without a second source of truth.
type Capability struct {
	Kind          contracts.Kind  `json:"kind"`
	Title         string          `json:"title"`
	Available     bool            `json:"available"`
	InputMimes    []string        `json:"input_mimes"`
	OutputFormats []string        `json:"output_formats"`
	Operations    []OperationInfo `json:"operations"`
}

func Capabilities() []Capability {
	return []Capability{
		{
			Kind:      contracts.KindImage,
			Title:     "Image",
			Available: true,
			InputMimes: []string{
				"image/jpeg", "image/png", "image/webp", "image/gif", "image/bmp", "image/tiff",
			},
			OutputFormats: []string{contracts.FormatJPEG, contracts.FormatPNG, contracts.FormatWebP},
			Operations: []OperationInfo{
				{
					Key:         contracts.OpConvert,
					Title:       "Convert",
					Description: "Re-encode an image as JPEG, PNG or WebP.",
				},
				{
					Key:         contracts.OpResize,
					Title:       "Resize",
					Description: "Scale an image to exact dimensions with contain, cover or fill.",
				},
				{
					Key:         contracts.OpCompress,
					Title:       "Compress",
					Description: "Shrink the file size by lowering quality and dropping metadata.",
				},
			},
		},
		{Kind: contracts.KindVideo, Title: "Video", Available: false},
		{Kind: contracts.KindAudio, Title: "Audio", Available: false},
		{Kind: contracts.KindDocument, Title: "Document", Available: false},
	}
}
