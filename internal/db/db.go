package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

const (
	connectRetryDelay = 2 * time.Second
	connectTimeout    = 60 * time.Second

	CollectionFiles = "files"
	CollectionJobs  = "jobs"
)

// Client wraps a MongoDB database and owns the underlying connection.
type Client struct {
	client *mongo.Client
	db     *mongo.Database
}

func (c *Client) Database() *mongo.Database { return c.db }

func (c *Client) Collection(name string) *mongo.Collection {
	return c.db.Collection(name)
}

func (c *Client) Ping(ctx context.Context) error {
	return c.client.Ping(ctx, readpref.Primary())
}

func (c *Client) Close(ctx context.Context) error {
	return c.client.Disconnect(ctx)
}

// Connect retries until MongoDB answers or connectTimeout elapses, so the API
// can start alongside a database that is still booting.
func Connect(ctx context.Context, uri, database string, log *slog.Logger) (*Client, error) {
	deadline := time.Now().Add(connectTimeout)
	for {
		client, err := tryConnect(ctx, uri, database)
		if err == nil {
			return client, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		log.Warn("waiting for mongodb", "error", err)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(connectRetryDelay):
		}
	}
}

func tryConnect(ctx context.Context, uri, database string) (*Client, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("connect mongodb: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, readpref.Primary()); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("ping mongodb: %w", err)
	}

	return &Client{client: client, db: client.Database(database)}, nil
}

// EnsureIndexes creates the indexes the repositories rely on. CreateMany is
// idempotent for matching definitions, so it is safe to run on every boot.
func EnsureIndexes(ctx context.Context, client *Client, log *slog.Logger) error {
	files := client.Collection(CollectionFiles)
	if _, err := files.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "expires_at", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("create files indexes: %w", err)
	}

	jobs := client.Collection(CollectionJobs)
	if _, err := jobs.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "file_id", Value: 1}}},
		{Keys: bson.D{{Key: "status", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("create jobs indexes: %w", err)
	}

	log.Info("mongodb indexes ready")
	return nil
}
