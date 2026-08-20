package httpapi

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/alikilicw/filecosystem-be/internal/config"
	"github.com/alikilicw/filecosystem-be/internal/db"
	"github.com/alikilicw/filecosystem-be/internal/queue"
	"github.com/alikilicw/filecosystem-be/internal/service"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

type Server struct {
	cfg    config.Config
	log    *slog.Logger
	files  *service.FileService
	images *service.ImageService
	queue  *queue.Client
	mongo  *db.Client
}

func NewServer(
	cfg config.Config,
	log *slog.Logger,
	files *service.FileService,
	images *service.ImageService,
	q *queue.Client,
	mongo *db.Client,
) *Server {
	return &Server{cfg: cfg, log: log, files: files, images: images, queue: q, mongo: mongo}
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(s.log))
	r.Use(middleware.Timeout(2 * time.Minute))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   splitAndTrim(s.cfg.AllowedOrigins),
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodOptions},
		AllowedHeaders:   []string{"Accept", "Content-Type", "X-Requested-With"},
		ExposedHeaders:   []string{"X-Request-Id"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleReady)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/capabilities", s.handleCapabilities)

		// Uploads go straight to S3: the API issues a presigned URL, the client
		// PUTs the bytes to the bucket, then reports back that it did. File
		// bytes never traverse this service.
		r.Route("/files", func(r chi.Router) {
			r.Post("/", s.handleCreateUpload)
			r.Post("/{fileID}/complete", s.handleCompleteUpload)
			r.Get("/{fileID}", s.handleGetFile)
			r.Get("/{fileID}/preview", s.handleFilePreview)
			r.Get("/{fileID}/jobs", s.handleListFileJobs)
		})

		// Each file family gets its own namespace so video and document
		// endpoints can be added without reshaping the existing routes.
		r.Route("/image", func(r chi.Router) {
			r.Post("/jobs", s.handleCreateImageJob)
		})

		r.Route("/jobs", func(r chi.Router) {
			r.Get("/{jobID}", s.handleGetJob)
			r.Get("/{jobID}/download", s.handleDownloadJob)
		})
	})

	return r
}

func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			log.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}

func splitAndTrim(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
