package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alikilicw/filecosystem-be/internal/config"
	"github.com/alikilicw/filecosystem-be/internal/contracts"
	"github.com/alikilicw/filecosystem-be/internal/db"
	"github.com/alikilicw/filecosystem-be/internal/httpapi"
	"github.com/alikilicw/filecosystem-be/internal/queue"
	"github.com/alikilicw/filecosystem-be/internal/repository"
	"github.com/alikilicw/filecosystem-be/internal/service"
	"github.com/alikilicw/filecosystem-be/internal/storage"
)

const (
	cleanupInterval  = 15 * time.Minute
	cleanupBatchSize = 200
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("api terminated", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mongo, err := db.Connect(ctx, cfg.MongoURI, cfg.MongoDatabase, log)
	if err != nil {
		return err
	}
	defer func() {
		disconnectCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mongo.Close(disconnectCtx)
	}()

	if err := db.EnsureIndexes(ctx, mongo, log); err != nil {
		return err
	}

	store := storage.New(cfg.S3)
	if err := store.EnsureBucket(ctx); err != nil {
		return err
	}

	fileRepo := repository.NewFileRepository(mongo)
	jobRepo := repository.NewJobRepository(mongo)

	broker := queue.New(cfg.RabbitURL, log)
	broker.RegisterConsumer(contracts.QueueJobResults, 16, 4, service.NewResultConsumer(jobRepo, log).Handle)
	if err := broker.Start(ctx); err != nil {
		return err
	}
	defer broker.Close()

	fileService := service.NewFileService(fileRepo, store, log, cfg.MaxUploadBytes, cfg.FileTTL, cfg.UploadTTL)
	imageService := service.NewImageService(fileRepo, jobRepo, broker, store, log)

	go runCleanup(ctx, fileService, log)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewServer(cfg, log, fileService, imageService, broker, mongo).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      3 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

// runCleanup keeps the object store from growing without bound. Two things
// accumulate: files that lived out their TTL along with the results derived from
// them, and uploads the direct-to-S3 handshake never finished.
func runCleanup(ctx context.Context, files *service.FileService, log *slog.Logger) {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if removed, err := files.PurgeExpired(ctx, cleanupBatchSize); err != nil {
				log.Error("expiry cleanup failed", "error", err)
			} else if removed > 0 {
				log.Info("expired objects removed", "count", removed)
			}

			if removed, err := files.PurgeAbandoned(ctx, cleanupBatchSize); err != nil {
				log.Error("abandoned upload cleanup failed", "error", err)
			} else if removed > 0 {
				log.Info("abandoned uploads removed", "count", removed)
			}
		}
	}
}
