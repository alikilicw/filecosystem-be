package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alikilicw/filecosystem-be/internal/contracts"
	"github.com/alikilicw/filecosystem-be/internal/db"
	"github.com/alikilicw/filecosystem-be/internal/domain"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type jobDocument struct {
	ID         string                `bson:"_id"`
	FileID     string                `bson:"file_id"`
	Kind       contracts.Kind        `bson:"kind"`
	Operation  contracts.Operation   `bson:"operation"`
	Status     contracts.Status      `bson:"status"`
	Params     contracts.ImageParams `bson:"params"`
	ResultName string                `bson:"result_name"`
	ResultKey  string                `bson:"result_key"`
	ResultSize int64                 `bson:"result_size"`
	ResultMime string                `bson:"result_mime"`
	Width      int                   `bson:"width"`
	Height     int                   `bson:"height"`
	Error      string                `bson:"error"`
	CreatedAt  time.Time             `bson:"created_at"`
	UpdatedAt  time.Time             `bson:"updated_at"`
	FinishedAt *time.Time            `bson:"finished_at,omitempty"`
}

type JobRepository struct {
	jobs *mongo.Collection
}

func NewJobRepository(client *db.Client) *JobRepository {
	return &JobRepository{jobs: client.Collection(db.CollectionJobs)}
}

func (r *JobRepository) Create(ctx context.Context, j domain.Job) error {
	if _, err := r.jobs.InsertOne(ctx, toJobDocument(j)); err != nil {
		return fmt.Errorf("insert job: %w", err)
	}
	return nil
}

func (r *JobRepository) Get(ctx context.Context, id uuid.UUID) (domain.Job, error) {
	var doc jobDocument
	err := r.jobs.FindOne(ctx, bson.M{"_id": id.String()}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return domain.Job{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Job{}, fmt.Errorf("select job: %w", err)
	}
	return fromJobDocument(doc)
}

func (r *JobRepository) ListByFile(ctx context.Context, fileID uuid.UUID) ([]domain.Job, error) {
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}})
	cursor, err := r.jobs.Find(ctx, bson.M{"file_id": fileID.String()}, opts)
	if err != nil {
		return nil, fmt.Errorf("select jobs: %w", err)
	}
	defer cursor.Close(ctx)

	jobs := make([]domain.Job, 0)
	for cursor.Next(ctx) {
		var doc jobDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		job, err := fromJobDocument(doc)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, cursor.Err()
}

// ApplyResult writes the terminal state reported by a worker. Already finished
// jobs are left untouched so a redelivered message cannot overwrite them.
func (r *JobRepository) ApplyResult(ctx context.Context, res contracts.JobResult) error {
	id, err := uuid.Parse(res.JobID)
	if err != nil {
		return fmt.Errorf("parse job id %q: %w", res.JobID, err)
	}

	finishedAt := res.FinishedAt
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}

	_, err = r.jobs.UpdateOne(ctx,
		bson.M{
			"_id": id.String(),
			"status": bson.M{"$nin": []contracts.Status{
				contracts.StatusSucceeded,
				contracts.StatusFailed,
			}},
		},
		bson.M{"$set": bson.M{
			"status":      res.Status,
			"result_key":  res.ResultKey,
			"result_name": res.ResultName,
			"result_size": res.ResultSize,
			"result_mime": res.ResultMime,
			"width":       res.Width,
			"height":      res.Height,
			"error":       res.Error,
			"finished_at": finishedAt,
			"updated_at":  time.Now().UTC(),
		}},
	)
	if err != nil {
		return fmt.Errorf("update job result: %w", err)
	}
	return nil
}

func (r *JobRepository) MarkFailed(ctx context.Context, id uuid.UUID, reason string) error {
	now := time.Now().UTC()
	_, err := r.jobs.UpdateOne(ctx,
		bson.M{
			"_id": id.String(),
			"status": bson.M{"$nin": []contracts.Status{
				contracts.StatusSucceeded,
				contracts.StatusFailed,
			}},
		},
		bson.M{"$set": bson.M{
			"status":      contracts.StatusFailed,
			"error":       reason,
			"finished_at": now,
			"updated_at":  now,
		}},
	)
	if err != nil {
		return fmt.Errorf("mark job failed: %w", err)
	}
	return nil
}

func toJobDocument(j domain.Job) jobDocument {
	return jobDocument{
		ID:         j.ID.String(),
		FileID:     j.FileID.String(),
		Kind:       j.Kind,
		Operation:  j.Operation,
		Status:     j.Status,
		Params:     j.Params,
		ResultName: j.ResultName,
		ResultKey:  j.ResultKey,
		ResultSize: j.ResultSize,
		ResultMime: j.ResultMime,
		Width:      j.Width,
		Height:     j.Height,
		Error:      j.Error,
		CreatedAt:  j.CreatedAt,
		UpdatedAt:  j.UpdatedAt,
		FinishedAt: j.FinishedAt,
	}
}

func fromJobDocument(doc jobDocument) (domain.Job, error) {
	id, err := uuid.Parse(doc.ID)
	if err != nil {
		return domain.Job{}, fmt.Errorf("parse job id: %w", err)
	}
	fileID, err := uuid.Parse(doc.FileID)
	if err != nil {
		return domain.Job{}, fmt.Errorf("parse file id: %w", err)
	}
	return domain.Job{
		ID:         id,
		FileID:     fileID,
		Kind:       doc.Kind,
		Operation:  doc.Operation,
		Status:     doc.Status,
		Params:     doc.Params,
		ResultName: doc.ResultName,
		ResultKey:  doc.ResultKey,
		ResultSize: doc.ResultSize,
		ResultMime: doc.ResultMime,
		Width:      doc.Width,
		Height:     doc.Height,
		Error:      doc.Error,
		CreatedAt:  doc.CreatedAt,
		UpdatedAt:  doc.UpdatedAt,
		FinishedAt: doc.FinishedAt,
	}, nil
}
