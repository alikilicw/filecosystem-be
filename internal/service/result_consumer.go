package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/alikilicw/filecosystem-be/internal/contracts"
	"github.com/alikilicw/filecosystem-be/internal/repository"
)

// ResultConsumer applies worker outcomes to the database. The API owns the
// schema, so workers never touch Postgres directly.
type ResultConsumer struct {
	jobs *repository.JobRepository
	log  *slog.Logger
}

func NewResultConsumer(jobs *repository.JobRepository, log *slog.Logger) *ResultConsumer {
	return &ResultConsumer{jobs: jobs, log: log}
}

func (c *ResultConsumer) Handle(ctx context.Context, body []byte) error {
	var result contracts.JobResult
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode job result: %w", err)
	}
	if result.JobID == "" {
		return fmt.Errorf("job result is missing job_id")
	}

	if err := c.jobs.ApplyResult(ctx, result); err != nil {
		return err
	}

	c.log.Info("job result applied",
		"job_id", result.JobID,
		"status", result.Status,
		"result_size", result.ResultSize,
		"error", result.Error,
	)
	return nil
}
