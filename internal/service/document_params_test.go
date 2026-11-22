package service

import (
	"slices"
	"strings"
	"testing"

	"github.com/alikilicw/filecosystem-be/internal/contracts"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestNormalizeDocStepOrganize(t *testing.T) {
	refs := func(pairs ...int) []contracts.PageRef {
		out := []contracts.PageRef{}
		for i := 0; i < len(pairs); i += 2 {
			out = append(out, contracts.PageRef{Page: pairs[i], Rotate: pairs[i+1]})
		}
		return out
	}

	cases := []struct {
		name    string
		pages   []contracts.PageRef
		wantErr bool
		want    []contracts.PageRef
	}{
		{name: "reorder", pages: refs(2, 0, 1, 0, 3, 0), want: refs(2, 0, 1, 0, 3, 0)},
		{name: "delete", pages: refs(1, 0, 3, 0), want: refs(1, 0, 3, 0)},
		{name: "rotate folds negative", pages: refs(1, -90, 2, 0, 3, 450), want: refs(1, 270, 2, 0, 3, 90)},
		{name: "identity is nothing", pages: refs(1, 0, 2, 0, 3, 360), wantErr: true},
		{name: "empty", pages: refs(), wantErr: true},
		{name: "out of range", pages: refs(4, 0), wantErr: true},
		{name: "duplicate", pages: refs(1, 0, 1, 0), wantErr: true},
		{name: "odd angle", pages: refs(1, 45), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeDocStep(contracts.DocumentStep{Op: contracts.DocOrganize, Pages: tc.pages, At: 2}, 3)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.At != 0 || got.Files != nil {
				t.Fatalf("organize kept fields of another op: %+v", got)
			}
			if len(got.Pages) != len(tc.want) {
				t.Fatalf("got %+v, want %+v", got.Pages, tc.want)
			}
			for i := range tc.want {
				if got.Pages[i] != tc.want[i] {
					t.Fatalf("got %+v, want %+v", got.Pages, tc.want)
				}
			}
		})
	}
}

func TestNormalizeDocStepInsert(t *testing.T) {
	id := "5f0c2a8e-8c51-4a51-9d5c-3d7f7b1b0e11"
	ok := contracts.DocumentStep{Op: contracts.DocInsert, Files: []string{id}, At: 3}
	if _, err := normalizeDocStep(ok, 3); err != nil {
		t.Fatalf("insert at the end: %v", err)
	}
	for name, step := range map[string]contracts.DocumentStep{
		"no files":   {Op: contracts.DocInsert, At: 0},
		"bad id":     {Op: contracts.DocInsert, Files: []string{"nope"}},
		"at too far": {Op: contracts.DocInsert, Files: []string{id}, At: 4},
		"negative":   {Op: contracts.DocInsert, Files: []string{id}, At: -1},
		"unknown op": {Op: "shuffle"},
	} {
		if _, err := normalizeDocStep(step, 3); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestNormalizeDocStepExports(t *testing.T) {
	cases := []struct {
		name    string
		step    contracts.DocumentStep
		pages   int
		wantErr bool
		check   func(contracts.DocumentStep) bool
	}{
		{name: "split", step: contracts.DocumentStep{Op: contracts.DocSplit, Cuts: []int{2, 4}, Pages: []contracts.PageRef{{Page: 1}}}, pages: 5,
			check: func(s contracts.DocumentStep) bool { return slices.Equal(s.Cuts, []int{2, 4}) && s.Pages == nil }},
		{name: "split without cuts", step: contracts.DocumentStep{Op: contracts.DocSplit}, pages: 5, wantErr: true},
		{name: "split after the last page", step: contracts.DocumentStep{Op: contracts.DocSplit, Cuts: []int{5}}, pages: 5, wantErr: true},
		{name: "split out of order", step: contracts.DocumentStep{Op: contracts.DocSplit, Cuts: []int{3, 2}}, pages: 5, wantErr: true},
		{name: "split one page", step: contracts.DocumentStep{Op: contracts.DocSplit, Cuts: []int{1}}, pages: 1, wantErr: true},

		{name: "images jpg alias", step: contracts.DocumentStep{Op: contracts.DocImages, Format: "JPG", DPI: 150, Select: []int{1, 3}}, pages: 3,
			check: func(s contracts.DocumentStep) bool {
				return s.Format == contracts.FormatJPEG && s.DPI == 150 && slices.Equal(s.Select, []int{1, 3})
			}},
		{name: "images every page is all", step: contracts.DocumentStep{Op: contracts.DocImages, Format: "png", DPI: 72, Select: []int{1, 2}}, pages: 2,
			check: func(s contracts.DocumentStep) bool { return s.Select == nil }},
		{name: "images bad format", step: contracts.DocumentStep{Op: contracts.DocImages, Format: "gif", DPI: 150}, pages: 2, wantErr: true},
		{name: "images dpi too high", step: contracts.DocumentStep{Op: contracts.DocImages, Format: "png", DPI: 600}, pages: 2, wantErr: true},
		{name: "images page out of range", step: contracts.DocumentStep{Op: contracts.DocImages, Format: "png", DPI: 150, Select: []int{3}}, pages: 2, wantErr: true},

		{name: "protect", step: contracts.DocumentStep{Op: contracts.DocProtect, Password: "s3cret", Cuts: []int{1}}, pages: 2,
			check: func(s contracts.DocumentStep) bool { return s.Password == "s3cret" && s.Cuts == nil }},
		{name: "protect empty", step: contracts.DocumentStep{Op: contracts.DocProtect}, pages: 2, wantErr: true},
		{name: "protect too long", step: contracts.DocumentStep{Op: contracts.DocProtect, Password: strings.Repeat("x", 128)}, pages: 2, wantErr: true},

		{name: "compress default level", step: contracts.DocumentStep{Op: contracts.DocCompress, DPI: 150}, pages: 1,
			check: func(s contracts.DocumentStep) bool { return s.Level == contracts.CompressRecommended && s.DPI == 0 }},
		{name: "compress strong", step: contracts.DocumentStep{Op: contracts.DocCompress, Level: "strong"}, pages: 1,
			check: func(s contracts.DocumentStep) bool { return s.Level == contracts.CompressStrong }},
		{name: "compress bad level", step: contracts.DocumentStep{Op: contracts.DocCompress, Level: "max"}, pages: 1, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeDocStep(tc.step, tc.pages)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !tc.check(got) {
				t.Fatalf("unexpected %+v", got)
			}
		})
	}
	if !contracts.DocSplit.Exports() || contracts.DocOrganize.Exports() || contracts.DocCompress.Exports() {
		t.Fatal("Exports is wrong")
	}
}

// A password travels in the job message only: the job stored in the database
// must not carry it.
func TestDocumentStepPasswordIsNotStored(t *testing.T) {
	raw, err := bson.Marshal(contracts.DocumentStep{Op: contracts.DocProtect, Password: "hunter2-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2-secret") {
		t.Fatal("the password was written to BSON")
	}
}
