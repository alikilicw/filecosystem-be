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
	ID              string                  `bson:"_id"`
	FileID          string                  `bson:"file_id"`
	SessionID       string                  `bson:"session_id"`
	ParentID        string                  `bson:"parent_id,omitempty"`
	Seq             int                     `bson:"seq"`
	Kind            contracts.Kind          `bson:"kind"`
	Operation       contracts.Operation     `bson:"operation"`
	Status          contracts.Status        `bson:"status"`
	Step            *contracts.ImageStep    `bson:"step,omitempty"`
	DocStep         *contracts.DocumentStep `bson:"doc_step,omitempty"`
	Pages           []contracts.PageInfo    `bson:"pages,omitempty"`
	Encode          *contracts.Encode       `bson:"encode,omitempty"`
	Animation       *contracts.Animation    `bson:"animation,omitempty"`
	IntermediateKey string                  `bson:"intermediate_key"`
	PreviewKey      string                  `bson:"preview_key"`
	ResultName      string                  `bson:"result_name"`
	ResultKey       string                  `bson:"result_key"`
	ResultSize      int64                   `bson:"result_size"`
	ResultMime      string                  `bson:"result_mime"`
	Width           int                     `bson:"width"`
	Height          int                     `bson:"height"`
	Error           string                  `bson:"error"`
	ErrorCode       string                  `bson:"error_code,omitempty"`
	CreatedAt       time.Time               `bson:"created_at"`
	UpdatedAt       time.Time               `bson:"updated_at"`
	FinishedAt      *time.Time              `bson:"finished_at,omitempty"`
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

// ListBySession returns every version of a session, oldest first.
func (r *JobRepository) ListBySession(ctx context.Context, sessionID uuid.UUID) ([]domain.Job, error) {
	opts := options.Find().SetSort(bson.D{{Key: "seq", Value: 1}}).SetLimit(contracts.MaxVersions + 1)
	cursor, err := r.jobs.Find(ctx, bson.M{"session_id": sessionID.String()}, opts)
	if err != nil {
		return nil, fmt.Errorf("select session jobs: %w", err)
	}
	defer cursor.Close(ctx)

	var out []domain.Job
	for cursor.Next(ctx) {
		var doc jobDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		j, err := fromJobDocument(doc)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, cursor.Err()
}

// ApplyResult writes the terminal state reported by a worker and returns the
// updated job. ok is false when the job had already finished, so a
// redelivered message changes nothing.
func (r *JobRepository) ApplyResult(ctx context.Context, res contracts.JobResult) (domain.Job, bool, error) {
	id, err := uuid.Parse(res.JobID)
	if err != nil {
		return domain.Job{}, false, fmt.Errorf("parse job id %q: %w", res.JobID, err)
	}

	finishedAt := res.FinishedAt
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}

	set := bson.M{
		"status":      res.Status,
		"error":       res.Error,
		"error_code":  res.Code,
		"finished_at": finishedAt,
		"updated_at":  time.Now().UTC(),
	}
	if res.Status == contracts.StatusSucceeded {
		set["result_key"] = res.ResultKey
		set["result_name"] = res.ResultName
		set["result_size"] = res.ResultSize
		set["result_mime"] = res.ResultMime
		set["intermediate_key"] = res.IntermediateKey
		set["preview_key"] = res.PreviewKey
		set["width"] = res.Width
		set["height"] = res.Height
		set["animation"] = res.Animation
		if res.Encode != nil {
			set["encode"] = res.Encode
		}
		if res.Pages != nil {
			set["pages"] = res.Pages
		}
	}

	var doc jobDocument
	err = r.jobs.FindOneAndUpdate(ctx,
		bson.M{"_id": id.String(), "status": bson.M{"$nin": terminal}},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return domain.Job{}, false, nil
	}
	if err != nil {
		return domain.Job{}, false, fmt.Errorf("update job result: %w", err)
	}
	j, err := fromJobDocument(doc)
	return j, err == nil, err
}

func (r *JobRepository) MarkFailed(ctx context.Context, id uuid.UUID, reason string) error {
	now := time.Now().UTC()
	_, err := r.jobs.UpdateOne(ctx,
		bson.M{"_id": id.String(), "status": bson.M{"$nin": terminal}},
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

var terminal = []contracts.Status{contracts.StatusSucceeded, contracts.StatusFailed}

func toJobDocument(j domain.Job) jobDocument {
	doc := jobDocument{
		ID:              j.ID.String(),
		FileID:          j.FileID.String(),
		SessionID:       j.SessionID.String(),
		Seq:             j.Seq,
		Kind:            j.Kind,
		Operation:       j.Operation,
		Status:          j.Status,
		Step:            j.Step,
		DocStep:         j.DocStep,
		Pages:           j.Pages,
		Encode:          j.Encode,
		Animation:       j.Animation,
		IntermediateKey: j.IntermediateKey,
		PreviewKey:      j.PreviewKey,
		ResultName:      j.ResultName,
		ResultKey:       j.ResultKey,
		ResultSize:      j.ResultSize,
		ResultMime:      j.ResultMime,
		Width:           j.Width,
		Height:          j.Height,
		Error:           j.Error,
		ErrorCode:       j.ErrorCode,
		CreatedAt:       j.CreatedAt,
		UpdatedAt:       j.UpdatedAt,
		FinishedAt:      j.FinishedAt,
	}
	if j.ParentID != uuid.Nil {
		doc.ParentID = j.ParentID.String()
	}
	return doc
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
	sessionID, err := uuid.Parse(doc.SessionID)
	if err != nil {
		return domain.Job{}, fmt.Errorf("parse session id: %w", err)
	}
	var parentID uuid.UUID
	if doc.ParentID != "" {
		if parentID, err = uuid.Parse(doc.ParentID); err != nil {
			return domain.Job{}, fmt.Errorf("parse parent id: %w", err)
		}
	}
	return domain.Job{
		ID:              id,
		FileID:          fileID,
		SessionID:       sessionID,
		ParentID:        parentID,
		Seq:             doc.Seq,
		Kind:            doc.Kind,
		Operation:       doc.Operation,
		Status:          doc.Status,
		Step:            doc.Step,
		DocStep:         doc.DocStep,
		Pages:           doc.Pages,
		Encode:          doc.Encode,
		Animation:       doc.Animation,
		IntermediateKey: doc.IntermediateKey,
		PreviewKey:      doc.PreviewKey,
		ResultName:      doc.ResultName,
		ResultKey:       doc.ResultKey,
		ResultSize:      doc.ResultSize,
		ResultMime:      doc.ResultMime,
		Width:           doc.Width,
		Height:          doc.Height,
		Error:           doc.Error,
		ErrorCode:       doc.ErrorCode,
		CreatedAt:       doc.CreatedAt,
		UpdatedAt:       doc.UpdatedAt,
		FinishedAt:      doc.FinishedAt,
	}, nil
}
