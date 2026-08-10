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

// uploadMemoryLimit is how much of a multipart body is buffered in RAM before
// the rest spills to a temporary file.
const uploadMemoryLimit = 8 << 20

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

func (s *Server) handleCapabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"capabilities":     service.Capabilities(),
		"max_upload_bytes": s.files.MaxSize(),
		"file_ttl_seconds": int(s.cfg.FileTTL.Seconds()),
	})
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	maxSize := s.files.MaxSize()
	// The multipart envelope adds overhead on top of the payload itself, so the
	// hard limit is slightly above the configured file size.
	r.Body = http.MaxBytesReader(w, r.Body, maxSize+uploadMemoryLimit)

	if err := r.ParseMultipartForm(uploadMemoryLimit); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "The uploaded file is too large.")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_multipart", "Expected a multipart form with a 'file' field.")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	_, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing_file", "The 'file' form field is required.")
		return
	}

	file, err := s.files.Upload(r.Context(), header)
	if err != nil {
		writeServiceError(w, s.log, err)
		return
	}

	previewURL, err := s.files.PreviewURL(r.Context(), file)
	if err != nil {
		s.log.Error("failed to presign preview", "file_id", file.ID, "error", err)
	}

	writeJSON(w, http.StatusCreated, map[string]any{"file": file, "preview_url": previewURL})
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
