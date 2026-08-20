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

// IDs are stored as strings so documents stay readable in Compass and avoid
// Binary UUID subtype mismatches across driver versions.
type fileDocument struct {
	ID           string               `bson:"_id"`
	OriginalName string               `bson:"original_name"`
	StorageKey   string               `bson:"storage_key"`
	MimeType     string               `bson:"mime_type"`
	SizeBytes    int64                `bson:"size_bytes"`
	Kind         contracts.Kind       `bson:"kind"`
	Status       contracts.FileStatus `bson:"status"`
	CreatedAt    time.Time            `bson:"created_at"`
	UpdatedAt    time.Time            `bson:"updated_at"`
	ExpiresAt    time.Time            `bson:"expires_at"`
}

type FileRepository struct {
	files *mongo.Collection
	jobs  *mongo.Collection
}

func NewFileRepository(client *db.Client) *FileRepository {
	return &FileRepository{
		files: client.Collection(db.CollectionFiles),
		jobs:  client.Collection(db.CollectionJobs),
	}
}

func (r *FileRepository) Create(ctx context.Context, f domain.File) error {
	if _, err := r.files.InsertOne(ctx, toFileDocument(f)); err != nil {
		return fmt.Errorf("insert file: %w", err)
	}
	return nil
}

func (r *FileRepository) Get(ctx context.Context, id uuid.UUID) (domain.File, error) {
	var doc fileDocument
	err := r.files.FindOne(ctx, bson.M{"_id": id.String()}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return domain.File{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.File{}, fmt.Errorf("select file: %w", err)
	}
	return fromFileDocument(doc)
}

// MarkReady moves a file out of pending and reports whether this call is the one
// that did it. The guard makes a duplicate complete request cheap and keeps the
// timestamps honest when a client reports the same upload twice.
func (r *FileRepository) MarkReady(ctx context.Context, id uuid.UUID) (bool, error) {
	res, err := r.files.UpdateOne(ctx,
		bson.M{"_id": id.String(), "status": contracts.FileStatusPending},
		bson.M{"$set": bson.M{
			"status":     contracts.FileStatusReady,
			"updated_at": time.Now().UTC(),
		}},
	)
	if err != nil {
		return false, fmt.Errorf("mark file ready: %w", err)
	}
	return res.MatchedCount == 1, nil
}

// DeleteAbandoned drops files that never reached a usable state and returns the
// storage keys to purge: tickets the client never uploaded against, or uploaded
// against but never reported.
func (r *FileRepository) DeleteAbandoned(ctx context.Context, cutoff time.Time, limit int) ([]string, error) {
	filter := bson.M{
		"status":     contracts.FileStatusPending,
		"updated_at": bson.M{"$lt": cutoff},
	}

	opts := options.Find().SetSort(bson.D{{Key: "updated_at", Value: 1}}).SetLimit(int64(limit))
	cursor, err := r.files.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("select abandoned files: %w", err)
	}
	defer cursor.Close(ctx)

	var ids, keys []string
	for cursor.Next(ctx) {
		var doc fileDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("scan abandoned file: %w", err)
		}
		ids = append(ids, doc.ID)
		keys = append(keys, doc.StorageKey)
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	if _, err := r.files.DeleteMany(ctx, bson.M{"_id": bson.M{"$in": ids}}); err != nil {
		return nil, fmt.Errorf("delete abandoned files: %w", err)
	}
	return keys, nil
}

// DeleteExpired removes metadata for files past their TTL and returns every
// storage key the caller should purge, including the results derived from them.
func (r *FileRepository) DeleteExpired(ctx context.Context, limit int) ([]string, error) {
	opts := options.Find().SetSort(bson.D{{Key: "expires_at", Value: 1}}).SetLimit(int64(limit))
	cursor, err := r.files.Find(ctx, bson.M{"expires_at": bson.M{"$lt": time.Now().UTC()}}, opts)
	if err != nil {
		return nil, fmt.Errorf("select expired files: %w", err)
	}
	defer cursor.Close(ctx)

	var (
		ids  []string
		keys []string
	)
	for cursor.Next(ctx) {
		var doc fileDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("scan expired file: %w", err)
		}
		ids = append(ids, doc.ID)
		keys = append(keys, doc.StorageKey)
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	resultCursor, err := r.jobs.Find(ctx, bson.M{
		"file_id":    bson.M{"$in": ids},
		"result_key": bson.M{"$ne": ""},
	})
	if err != nil {
		return nil, fmt.Errorf("select expired results: %w", err)
	}
	defer resultCursor.Close(ctx)

	for resultCursor.Next(ctx) {
		var job jobDocument
		if err := resultCursor.Decode(&job); err != nil {
			return nil, fmt.Errorf("scan expired result: %w", err)
		}
		keys = append(keys, job.ResultKey)
	}
	if err := resultCursor.Err(); err != nil {
		return nil, err
	}

	if _, err := r.jobs.DeleteMany(ctx, bson.M{"file_id": bson.M{"$in": ids}}); err != nil {
		return nil, fmt.Errorf("delete expired jobs: %w", err)
	}
	if _, err := r.files.DeleteMany(ctx, bson.M{"_id": bson.M{"$in": ids}}); err != nil {
		return nil, fmt.Errorf("delete expired files: %w", err)
	}
	return keys, nil
}

func toFileDocument(f domain.File) fileDocument {
	return fileDocument{
		ID:           f.ID.String(),
		OriginalName: f.OriginalName,
		StorageKey:   f.StorageKey,
		MimeType:     f.MimeType,
		SizeBytes:    f.SizeBytes,
		Kind:         f.Kind,
		Status:       f.Status,
		CreatedAt:    f.CreatedAt,
		UpdatedAt:    f.UpdatedAt,
		ExpiresAt:    f.ExpiresAt,
	}
}

func fromFileDocument(doc fileDocument) (domain.File, error) {
	id, err := uuid.Parse(doc.ID)
	if err != nil {
		return domain.File{}, fmt.Errorf("parse file id: %w", err)
	}
	return domain.File{
		ID:           id,
		OriginalName: doc.OriginalName,
		StorageKey:   doc.StorageKey,
		MimeType:     doc.MimeType,
		SizeBytes:    doc.SizeBytes,
		Kind:         doc.Kind,
		Status:       doc.Status,
		CreatedAt:    doc.CreatedAt,
		UpdatedAt:    doc.UpdatedAt,
		ExpiresAt:    doc.ExpiresAt,
	}, nil
}
