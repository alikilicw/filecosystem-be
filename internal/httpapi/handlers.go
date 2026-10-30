package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/alikilicw/filecosystem-be/internal/contracts"
	"github.com/alikilicw/filecosystem-be/internal/service"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	checks := map[string]bool{
		"database": s.mongo.Ping(ctx) == nil,
		"queue":    s.queue.Healthy(),
	}

	status := http.StatusOK
	for _, ok := range checks {
		if !ok {
			status = http.StatusServiceUnavailable
		}
	}
	writeJSON(w, status, map[string]any{"ready": status == http.StatusOK, "checks": checks})
}

type createUploadRequest struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

// handleCreateUpload issues the presigned form the browser POSTs the bytes to:
// upload_fields go into a multipart form, followed by the file as the last
// field, named "file". No file bytes ever reach this process.
func (s *Server) handleCreateUpload(w http.ResponseWriter, r *http.Request) {
	var req createUploadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}

	file, upload, err := s.files.CreateUpload(r.Context(), service.CreateUploadInput{
		Filename:    req.Filename,
		ContentType: req.ContentType,
		SizeBytes:   req.SizeBytes,
	})
	if err != nil {
		writeServiceError(w, s.log, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"file":              file,
		"upload_url":        upload.URL,
		"upload_fields":     upload.Fields,
		"upload_expires_at": upload.ExpiresAt,
	})
}

// handleCompleteUpload is the client saying its S3 upload finished, which is
// what makes the file usable as a job source. Calling it twice is safe.
func (s *Server) handleCompleteUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(w, chi.URLParam(r, "fileID"), "file_id")
	if !ok {
		return
	}

	file, err := s.files.CompleteUpload(r.Context(), id)
	if err != nil {
		writeServiceError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"file": file})
}

func (s *Server) handleGetFile(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(w, chi.URLParam(r, "fileID"), "file_id")
	if !ok {
		return
	}

	file, err := s.files.Get(r.Context(), id)
	if err != nil {
		writeServiceError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"file": file})
}

func (s *Server) handleFilePreview(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(w, chi.URLParam(r, "fileID"), "file_id")
	if !ok {
		return
	}

	file, err := s.files.Get(r.Context(), id)
	if err != nil {
		writeServiceError(w, s.log, err)
		return
	}

	url, err := s.files.PreviewURL(r.Context(), file)
	if err != nil {
		writeServiceError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url})
}

func (s *Server) handleListFileJobs(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(w, chi.URLParam(r, "fileID"), "file_id")
	if !ok {
		return
	}

	jobs, err := s.images.ListJobsByFile(r.Context(), id)
	if err != nil {
		writeServiceError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

type createImageJobRequest struct {
	FileID    string                `json:"file_id"`
	Operation contracts.Operation   `json:"operation"`
	Params    contracts.ImageParams `json:"params"`
}

func (s *Server) handleCreateImageJob(w http.ResponseWriter, r *http.Request) {
	var req createImageJobRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}

	fileID, ok := parseUUID(w, req.FileID, "file_id")
	if !ok {
		return
	}

	job, err := s.images.CreateJob(r.Context(), service.CreateJobInput{
		FileID:    fileID,
		Operation: req.Operation,
		Params:    req.Params,
	})
	if err != nil {
		writeServiceError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(w, chi.URLParam(r, "jobID"), "job_id")
	if !ok {
		return
	}

	job, err := s.images.GetJob(r.Context(), id)
	if err != nil {
		writeServiceError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": job})
}

func (s *Server) handleDownloadJob(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(w, chi.URLParam(r, "jobID"), "job_id")
	if !ok {
		return
	}

	url, err := s.images.DownloadURL(r.Context(), id)
	if err != nil {
		writeServiceError(w, s.log, err)
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.New("request body is not valid JSON for this endpoint")
	}
	return nil
}

func parseUUID(w http.ResponseWriter, raw, field string) (uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_"+field, field+" must be a UUID")
		return uuid.Nil, false
	}
	return id, true
}
