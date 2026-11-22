package contracts

import "testing"

func TestOfficeMimes(t *testing.T) {
	for _, tc := range []struct {
		mime   string
		ext    string
		office bool
	}{
		{"application/pdf", ".pdf", false},
		{"image/png", ".png", false},
		{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".docx", true},
		{"application/vnd.ms-excel; charset=binary", ".xls", true},
		{"Application/VND.oasis.opendocument.presentation", ".odp", true},
	} {
		kind, ext, ok := InputMime(tc.mime)
		if !ok {
			t.Fatalf("%s: not accepted", tc.mime)
		}
		if tc.mime != "image/png" && kind != KindDocument {
			t.Errorf("%s: kind %s", tc.mime, kind)
		}
		if ext != tc.ext {
			t.Errorf("%s: ext %s, want %s", tc.mime, ext, tc.ext)
		}
		if IsOffice(tc.mime) != tc.office {
			t.Errorf("%s: IsOffice = %v", tc.mime, !tc.office)
		}
	}
}
