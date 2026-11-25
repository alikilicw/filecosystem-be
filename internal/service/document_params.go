package service

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alikilicw/filecosystem-be/internal/contracts"
	"github.com/google/uuid"
)

// normalizeDocStep checks one document step against the head's page count
// and keeps only the fields that apply to its op, so a bad request fails here
// with a clear message instead of in a worker.
func normalizeDocStep(s contracts.DocumentStep, pageCount int) (contracts.DocumentStep, error) {
	out := contracts.DocumentStep{Op: s.Op}

	switch s.Op {
	case contracts.DocOrganize:
		if len(s.Pages) == 0 {
			return out, fmt.Errorf("a document needs at least one page")
		}
		if len(s.Pages) > pageCount {
			return out, fmt.Errorf("the document has only %d pages", pageCount)
		}
		seen := make(map[int]bool, len(s.Pages))
		changed := len(s.Pages) != pageCount
		out.Pages = make([]contracts.PageRef, len(s.Pages))
		for i, ref := range s.Pages {
			if ref.Page < 1 || ref.Page > pageCount {
				return out, fmt.Errorf("page %d does not exist", ref.Page)
			}
			if seen[ref.Page] {
				return out, fmt.Errorf("page %d is listed twice", ref.Page)
			}
			seen[ref.Page] = true
			if ref.Rotate%90 != 0 {
				return out, fmt.Errorf("rotation must be a multiple of 90 degrees")
			}
			rotate := ((ref.Rotate % 360) + 360) % 360
			if ref.Page != i+1 || rotate != 0 {
				changed = true
			}
			out.Pages[i] = contracts.PageRef{Page: ref.Page, Rotate: rotate}
		}
		if !changed {
			return out, fmt.Errorf("nothing to change")
		}

	case contracts.DocInsert:
		if len(s.Files) == 0 {
			return out, fmt.Errorf("at least one file is required")
		}
		if len(s.Files) > contracts.MaxDocumentInputs {
			return out, fmt.Errorf("at most %d files can be added at once", contracts.MaxDocumentInputs)
		}
		if s.At < 0 || s.At > pageCount {
			return out, fmt.Errorf("at must be between 0 and %d", pageCount)
		}
		out.Files = make([]string, len(s.Files))
		for i, raw := range s.Files {
			id, err := uuid.Parse(raw)
			if err != nil {
				return out, fmt.Errorf("%q is not a file id", raw)
			}
			out.Files[i] = id.String()
		}
		out.At = s.At

	case contracts.DocSplit:
		if pageCount < 2 {
			return out, fmt.Errorf("a one-page document cannot be split")
		}
		if len(s.Cuts) == 0 {
			return out, fmt.Errorf("choose where to split")
		}
		last := 0
		for _, cut := range s.Cuts {
			if cut <= last || cut >= pageCount {
				return out, fmt.Errorf("cuts must be ascending pages between 1 and %d", pageCount-1)
			}
			last = cut
		}
		out.Cuts = append([]int(nil), s.Cuts...)

	case contracts.DocImages:
		switch strings.ToLower(s.Format) {
		case contracts.FormatJPEG, "jpg":
			out.Format = contracts.FormatJPEG
		case contracts.FormatPNG:
			out.Format = contracts.FormatPNG
		default:
			return out, fmt.Errorf("format must be jpeg or png")
		}
		if s.DPI < contracts.MinImageDPI || s.DPI > contracts.MaxImageDPI {
			return out, fmt.Errorf("dpi must be between %d and %d", contracts.MinImageDPI, contracts.MaxImageDPI)
		}
		out.DPI = s.DPI
		last := 0
		for _, page := range s.Select {
			if page <= last || page > pageCount {
				return out, fmt.Errorf("selected pages must be ascending pages between 1 and %d", pageCount)
			}
			last = page
		}
		// Every page selected is the same as none: the whole document.
		if len(s.Select) < pageCount {
			out.Select = append([]int(nil), s.Select...)
		}

	case contracts.DocProtect:
		if s.Password == "" {
			return out, fmt.Errorf("a password is required")
		}
		if len(s.Password) > contracts.MaxPasswordBytes {
			return out, fmt.Errorf("a password has at most %d bytes", contracts.MaxPasswordBytes)
		}
		out.Password = s.Password

	case contracts.DocCompress:
		switch s.Level {
		case contracts.CompressLight, contracts.CompressRecommended, contracts.CompressStrong:
			out.Level = s.Level
		case "":
			out.Level = contracts.CompressRecommended
		default:
			return out, fmt.Errorf("level must be light, recommended or strong")
		}

	case contracts.DocPageNumbers:
		text, err := stampText(s.Text, "{n}")
		if err != nil {
			return out, err
		}
		if !strings.Contains(text, "{n}") {
			return out, fmt.Errorf("the text must contain {n} where the number goes")
		}
		out.Text = text
		out.Position = s.Position
		if out.Position == "" {
			out.Position = "bc"
		}
		if !slices.Contains(contracts.NumberPositions, out.Position) {
			return out, fmt.Errorf("position must be one of %s", strings.Join(contracts.NumberPositions, ", "))
		}
		out.Start = s.Start
		if out.Start == 0 {
			out.Start = 1
		}
		if out.Start < 1 || out.Start > contracts.MaxStart {
			return out, fmt.Errorf("start must be between 1 and %d", contracts.MaxStart)
		}
		if s.SkipFirst && pageCount < 2 {
			return out, fmt.Errorf("a one-page document has no page after the first to number")
		}
		out.SkipFirst = s.SkipFirst
		if out.Size, err = stampSize(s.Size); err != nil {
			return out, err
		}

	case contracts.DocWatermark:
		text, err := stampText(s.Text, "")
		if err != nil {
			return out, err
		}
		if text == "" {
			return out, fmt.Errorf("enter the watermark text")
		}
		out.Text = text
		if out.Size, err = stampSize(s.Size); err != nil {
			return out, err
		}
		out.Color = strings.ToLower(s.Color)
		if out.Color == "" {
			out.Color = "#808080"
		}
		if !hexColor.MatchString(out.Color) {
			return out, fmt.Errorf("color must be #rrggbb")
		}
		out.Opacity = s.Opacity
		if out.Opacity == 0 {
			out.Opacity = 0.3
		}
		if out.Opacity < contracts.MinOpacity || out.Opacity > 1 {
			return out, fmt.Errorf("opacity must be between %.1f and 1", contracts.MinOpacity)
		}
		out.Diagonal = s.Diagonal

	default:
		return out, fmt.Errorf("unknown op %q", s.Op)
	}
	return out, nil
}

var hexColor = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// stampText trims the text drawn onto pages, falling back to fallback when
// it is empty, and holds it to one line of MaxStampText characters.
func stampText(raw, fallback string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		text = fallback
	}
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("the text is not valid UTF-8")
	}
	if n := utf8.RuneCountInString(text); n > contracts.MaxStampText {
		return "", fmt.Errorf("the text has at most %d characters", contracts.MaxStampText)
	}
	if strings.IndexFunc(text, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("the text must be a single line")
	}
	return text, nil
}

func stampSize(size string) (string, error) {
	switch size {
	case "":
		return contracts.SizeMedium, nil
	case contracts.SizeSmall, contracts.SizeMedium, contracts.SizeLarge:
		return size, nil
	}
	return "", fmt.Errorf("size must be small, medium or large")
}
