package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Env             string
	HTTPAddr        string
	AllowedOrigins  string
	PublicBaseURL   string
	MaxUploadBytes  int64
	FileTTL         time.Duration
	ShutdownTimeout time.Duration

	MongoURI      string
	MongoDatabase string
	RabbitURL     string

	S3 S3Config
}

type S3Config struct {
	Endpoint       string
	PublicEndpoint string
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
	PresignTTL     time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		Env:             env("APP_ENV", "development"),
		HTTPAddr:        env("HTTP_ADDR", ":8080"),
		AllowedOrigins:  env("ALLOWED_ORIGINS", "http://localhost:3000"),
		PublicBaseURL:   env("PUBLIC_BASE_URL", "http://localhost:8080"),
		MaxUploadBytes:  envInt64("MAX_UPLOAD_BYTES", 50<<20),
		FileTTL:         envDuration("FILE_TTL", 24*time.Hour),
		ShutdownTimeout: envDuration("SHUTDOWN_TIMEOUT", 15*time.Second),
		MongoURI:        env("MONGO_URI", ""),
		MongoDatabase:   env("MONGO_DATABASE", "filecosystem"),
		RabbitURL:       env("RABBITMQ_URL", ""),
		S3: S3Config{
			Endpoint:       env("S3_ENDPOINT", ""),
			PublicEndpoint: env("S3_PUBLIC_ENDPOINT", ""),
			Region:         env("S3_REGION", "us-east-1"),
			Bucket:         env("S3_BUCKET", ""),
			AccessKey:      env("S3_ACCESS_KEY", ""),
			SecretKey:      env("S3_SECRET_KEY", ""),
			PresignTTL:     envDuration("S3_PRESIGN_TTL", time.Hour),
		},
	}

	for key, value := range map[string]string{
		"MONGO_URI":     cfg.MongoURI,
		"RABBITMQ_URL":  cfg.RabbitURL,
		"S3_BUCKET":     cfg.S3.Bucket,
		"S3_ACCESS_KEY": cfg.S3.AccessKey,
		"S3_SECRET_KEY": cfg.S3.SecretKey,
	} {
		if value == "" {
			return Config{}, fmt.Errorf("config: %s is required", key)
		}
	}

	return cfg, nil
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envInt64(key string, fallback int64) int64 {
	v, err := strconv.ParseInt(env(key, ""), 10, 64)
	if err != nil {
		return fallback
	}
	return v
}

func envBool(key string, fallback bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return v
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v, err := time.ParseDuration(env(key, ""))
	if err != nil {
		return fallback
	}
	return v
}
