package storage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alikilicw/filecosystem-be/internal/config"
)

func TestPresignPostPinsKeyTypeAndSize(t *testing.T) {
	s := New(config.S3Config{
		Region:     "eu-central-1",
		Bucket:     "test-bucket",
		AccessKey:  "AKIDEXAMPLE",
		SecretKey:  "secret",
		PresignTTL: time.Hour,
	})

	const (
		key         = "uploads/abc/photo.png"
		contentType = "image/png"
		size        = int64(12345)
	)
	up, err := s.PresignPost(context.Background(), key, contentType, size, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	if up.URL != "https://test-bucket.s3.eu-central-1.amazonaws.com" {
		t.Errorf("URL = %q", up.URL)
	}
	if up.Fields["key"] != key {
		t.Errorf("key field = %q, want %q", up.Fields["key"], key)
	}
	if up.Fields["Content-Type"] != contentType {
		t.Errorf("Content-Type field = %q, want %q", up.Fields["Content-Type"], contentType)
	}
	if up.Fields["X-Amz-Signature"] == "" {
		t.Error("missing signature field")
	}

	raw, err := base64.StdEncoding.DecodeString(up.Fields["policy"])
	if err != nil {
		t.Fatalf("decode policy: %v", err)
	}
	var policy struct {
		Conditions []json.RawMessage `json:"conditions"`
	}
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatalf("parse policy: %v", err)
	}

	var gotRange, gotType, gotKey bool
	for _, c := range policy.Conditions {
		var arr []any
		if json.Unmarshal(c, &arr) == nil {
			if len(arr) == 3 && arr[0] == "content-length-range" &&
				arr[1] == float64(size) && arr[2] == float64(size) {
				gotRange = true
			}
			continue
		}
		var obj map[string]string
		if json.Unmarshal(c, &obj) == nil {
			if obj["Content-Type"] == contentType {
				gotType = true
			}
			if obj["key"] == key {
				gotKey = true
			}
		}
	}
	if !gotRange {
		t.Errorf("policy has no content-length-range [%d, %d]: %s", size, size, raw)
	}
	if !gotType {
		t.Errorf("policy does not pin Content-Type: %s", raw)
	}
	if !gotKey {
		t.Errorf("policy does not pin key: %s", raw)
	}
}

func TestObjectURLUsesCDN(t *testing.T) {
	s := New(config.S3Config{
		Region:     "us-east-1",
		Bucket:     "test-bucket",
		AccessKey:  "AKIDEXAMPLE",
		SecretKey:  "secret",
		PresignTTL: time.Hour,
		CDNBaseURL: "https://cdn.filecosystem.com",
	})

	got, err := s.ObjectURL(context.Background(), "results/0b4e/photo 1-800x600.png", "photo 1-800x600.png")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://cdn.filecosystem.com/results/0b4e/photo%201-800x600.png"; got != want {
		t.Errorf("ObjectURL = %q, want %q", got, want)
	}
}

func TestObjectURLFallsBackToPresignedS3(t *testing.T) {
	s := New(config.S3Config{
		Region:     "us-east-1",
		Bucket:     "test-bucket",
		AccessKey:  "AKIDEXAMPLE",
		SecretKey:  "secret",
		PresignTTL: time.Hour,
	})

	got, err := s.ObjectURL(context.Background(), "results/0b4e/photo.png", "photo.png")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "test-bucket") || !strings.Contains(got, "X-Amz-Signature=") ||
		!strings.Contains(got, "response-content-disposition=") {
		t.Errorf("expected a presigned S3 URL with a download name, got %q", got)
	}
}
