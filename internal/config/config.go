package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Env             string
	HTTPAddr        string
	AllowedOrigins  string
	PublicBaseURL   string
	MaxUploadBytes  int64
	FileTTL         time.Duration
	UploadTTL       time.Duration
	ShutdownTimeout time.Duration

	// TrustProxyHeaders makes the API take the client IP from the X-Real-IP
	// header that nginx sets, as long as the request comes from a loopback or
	// private address. Turn it off if the API ever runs without nginx in front
	// on a private network, where other hosts could set the header themselves.
	TrustProxyHeaders bool
	RateLimit         RateLimitConfig

	MongoURI      string
	MongoDatabase string
	RabbitURL     string

	S3 S3Config
}

// RateLimitConfig caps requests per client IP. Writes create uploads and jobs;
// reads are mostly the browser polling job status every ~700ms, so they need
// far more room. A per-minute value of 0 turns that limit off.
type RateLimitConfig struct {
	WritePerMinute int
	WriteBurst     int
	ReadPerMinute  int
	ReadBurst      int
}

type S3Config struct {
	Endpoint       string
	PublicEndpoint string
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
	PresignTTL     time.Duration
	// CDNBaseURL serves stored files through a CDN in front of the private
	// bucket (e.g. https://cdn.filecosystem.com). Empty falls back to
	// presigned S3 URLs, which is what local development uses.
	CDNBaseURL string
}

func Load() (Config, error) {
	cfg := Config{
		Env:            env("APP_ENV", "development"),
		HTTPAddr:       env("HTTP_ADDR", ":8080"),
		AllowedOrigins: env("ALLOWED_ORIGINS", "http://localhost:3000"),
		PublicBaseURL:  env("PUBLIC_BASE_URL", "http://localhost:8080"),
		MaxUploadBytes: envInt64("MAX_UPLOAD_BYTES", 50<<20),
		FileTTL:        envDuration("FILE_TTL", 24*time.Hour),
		// How long a presigned upload ticket stays valid, and therefore how
		// long an upload that never completed is kept before being swept.
		UploadTTL:         envDuration("UPLOAD_TTL", 15*time.Minute),
		ShutdownTimeout:   envDuration("SHUTDOWN_TIMEOUT", 15*time.Second),
		TrustProxyHeaders: envBool("TRUST_PROXY_HEADERS", true),
		RateLimit: RateLimitConfig{
			WritePerMinute: envInt("RATE_LIMIT_WRITE_PER_MIN", 60),
			WriteBurst:     envInt("RATE_LIMIT_WRITE_BURST", 20),
			ReadPerMinute:  envInt("RATE_LIMIT_READ_PER_MIN", 300),
			ReadBurst:      envInt("RATE_LIMIT_READ_BURST", 60),
		},
		MongoURI:      env("MONGO_URI", ""),
		MongoDatabase: env("MONGO_DATABASE", "filecosystem"),
		RabbitURL:     env("RABBITMQ_URL", ""),
		S3: S3Config{
			Endpoint:       env("S3_ENDPOINT", ""),
			PublicEndpoint: env("S3_PUBLIC_ENDPOINT", ""),
			Region:         env("S3_REGION", "us-east-1"),
			Bucket:         env("S3_BUCKET", ""),
			AccessKey:      env("S3_ACCESS_KEY", ""),
			SecretKey:      env("S3_SECRET_KEY", ""),
			PresignTTL:     envDuration("S3_PRESIGN_TTL", time.Hour),
			CDNBaseURL:     strings.TrimRight(env("CDN_BASE_URL", ""), "/"),
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

func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(env(key, ""))
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
