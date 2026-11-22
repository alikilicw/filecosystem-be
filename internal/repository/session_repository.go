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

type sessionDocument struct {
	ID string `bson:"_id"`
	// Kind is empty on sessions made before documents existed, which were
	// all images.
	Kind          contracts.Kind `bson:"kind,omitempty"`
	FileID        string         `bson:"file_id"`
	SourceID      string         `bson:"source_id"`
	HeadID        string         `bson:"head_id"`
	NextSeq       int            `bson:"next_seq"`
	LockJobID     string         `bson:"lock_job_id"`
	LockExpiresAt time.Time      `bson:"lock_expires_at"`
	LastError     string         `bson:"last_error"`
	CreatedAt     time.Time      `bson:"created_at"`
	UpdatedAt     time.Time      `bson:"updated_at"`
}

// SessionRepository keeps the session documents. The lock is a field on the
// session taken with a single conditional update, so two requests racing for
// the same session cannot both start an operation.
type SessionRepository struct {
	sessions *mongo.Collection
}

func NewSessionRepository(client *db.Client) *SessionRepository {
	return &SessionRepository{sessions: client.Collection(db.CollectionSessions)}
}

func (r *SessionRepository) Create(ctx context.Context, s domain.Session) error {
	if _, err := r.sessions.InsertOne(ctx, toSessionDocument(s)); err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

func (r *SessionRepository) Get(ctx context.Context, id uuid.UUID) (domain.Session, error) {
	var doc sessionDocument
	err := r.sessions.FindOne(ctx, bson.M{"_id": id.String()}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return domain.Session{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Session{}, fmt.Errorf("select session: %w", err)
	}
	return fromSessionDocument(doc)
}

// idle matches a session with no operation running at now.
func idle(now time.Time) bson.M {
	return bson.M{"$or": bson.A{
		bson.M{"lock_job_id": ""},
		bson.M{"lock_expires_at": bson.M{"$lte": now}},
	}}
}

// Acquire locks an idle session for jobID and reserves the next version
// number. It returns the session as it is after the update; the reserved
// number is NextSeq-1. ErrBusy means another operation holds the lock.
func (r *SessionRepository) Acquire(ctx context.Context, id, jobID uuid.UUID, ttl time.Duration) (domain.Session, error) {
	now := time.Now().UTC()
	filter := idle(now)
	filter["_id"] = id.String()
	filter["next_seq"] = bson.M{"$lt": contracts.MaxVersions}

	var doc sessionDocument
	err := r.sessions.FindOneAndUpdate(ctx, filter,
		bson.M{
			"$set": bson.M{
				"lock_job_id":     jobID.String(),
				"lock_expires_at": now.Add(ttl),
				"last_error":      "",
				"updated_at":      now,
			},
			"$inc": bson.M{"next_seq": 1},
		},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		s, getErr := r.Get(ctx, id)
		if getErr != nil {
			return domain.Session{}, getErr
		}
		if s.NextSeq >= contracts.MaxVersions {
			return domain.Session{}, fmt.Errorf("%w: a session holds at most %d versions", domain.ErrInvalidInput, contracts.MaxVersions)
		}
		return domain.Session{}, domain.ErrBusy
	}
	if err != nil {
		return domain.Session{}, fmt.Errorf("lock session: %w", err)
	}
	return fromSessionDocument(doc)
}

// Finish releases the lock jobID holds. A succeeded job becomes the head; a
// failed one leaves the head where it was and records why. It does nothing
// when jobID no longer holds the lock (it lapsed and someone else took it).
func (r *SessionRepository) Finish(ctx context.Context, id, jobID uuid.UUID, succeeded bool, reason string) (bool, error) {
	set := bson.M{
		"lock_job_id": "",
		"last_error":  reason,
		"updated_at":  time.Now().UTC(),
	}
	if succeeded {
		set["head_id"] = jobID.String()
		set["last_error"] = ""
	}
	res, err := r.sessions.UpdateOne(ctx,
		bson.M{"_id": id.String(), "lock_job_id": jobID.String()},
		bson.M{"$set": set},
	)
	if err != nil {
		return false, fmt.Errorf("release session: %w", err)
	}
	return res.MatchedCount == 1, nil
}

// MoveHead points the head at another existing version, as long as the head
// is still from and nothing is running. It is how undo and reset work: no
// pixels are touched.
func (r *SessionRepository) MoveHead(ctx context.Context, id, from, to uuid.UUID) error {
	now := time.Now().UTC()
	filter := idle(now)
	filter["_id"] = id.String()
	filter["head_id"] = from.String()

	res, err := r.sessions.UpdateOne(ctx, filter, bson.M{"$set": bson.M{
		"head_id":    to.String(),
		"last_error": "",
		"updated_at": now,
	}})
	if err != nil {
		return fmt.Errorf("move session head: %w", err)
	}
	if res.MatchedCount == 0 {
		if _, err := r.Get(ctx, id); err != nil {
			return err
		}
		// Either an operation is running or the head moved under us.
		return domain.ErrBusy
	}
	return nil
}

func toSessionDocument(s domain.Session) sessionDocument {
	doc := sessionDocument{
		ID:            s.ID.String(),
		Kind:          s.Kind,
		FileID:        s.FileID.String(),
		SourceID:      s.SourceID.String(),
		HeadID:        s.HeadID.String(),
		NextSeq:       s.NextSeq,
		LockExpiresAt: s.LockExpiresAt,
		LastError:     s.LastError,
		CreatedAt:     s.CreatedAt,
		UpdatedAt:     s.UpdatedAt,
	}
	if s.LockJobID != uuid.Nil {
		doc.LockJobID = s.LockJobID.String()
	}
	return doc
}

func fromSessionDocument(doc sessionDocument) (domain.Session, error) {
	parse := func(name, raw string) (uuid.UUID, error) {
		if raw == "" {
			return uuid.Nil, nil
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			return uuid.Nil, fmt.Errorf("parse session %s: %w", name, err)
		}
		return id, nil
	}
	s := domain.Session{
		Kind:          doc.Kind,
		NextSeq:       doc.NextSeq,
		LockExpiresAt: doc.LockExpiresAt,
		LastError:     doc.LastError,
		CreatedAt:     doc.CreatedAt,
		UpdatedAt:     doc.UpdatedAt,
	}
	if s.Kind == "" {
		s.Kind = contracts.KindImage
	}
	var err error
	if s.ID, err = parse("id", doc.ID); err != nil {
		return s, err
	}
	if s.FileID, err = parse("file id", doc.FileID); err != nil {
		return s, err
	}
	if s.SourceID, err = parse("source id", doc.SourceID); err != nil {
		return s, err
	}
	if s.HeadID, err = parse("head id", doc.HeadID); err != nil {
		return s, err
	}
	if s.LockJobID, err = parse("lock job id", doc.LockJobID); err != nil {
		return s, err
	}
	return s, nil
}
