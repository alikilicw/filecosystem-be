# filecosystem-be

The Filecosystem API: a Go monolith that accepts uploads, queues transformation
jobs and hands back results. Heavy lifting happens in
[`filecosystem-workers`](../filecosystem-workers); the two talk over RabbitMQ.

```
browser ──HTTP──> api ──job.image──> rabbitmq ──> worker
                   ^                                 │
                   └────────── job.result ───────────┘
```

The API owns MongoDB and object storage metadata. Workers never touch the
database; they report outcomes as events and the API applies them.

## Requirements

- Go 1.25+
- Docker (MongoDB and RabbitMQ run in containers)
- An Amazon S3 bucket and credentials

## Getting started

```bash
cp .env.example .env   # fill in S3_BUCKET, S3_ACCESS_KEY, S3_SECRET_KEY, S3_REGION
make infra-up          # mongodb + rabbitmq
make run               # API on http://localhost:8080
```

In another terminal, start a worker from the `filecosystem-workers` repository
(same S3 settings in its `.env`).

To run everything in containers instead:

```bash
make apps-up           # builds and starts the API and a worker too
make logs
```

| Service          | URL                                            |
| ---------------- | ---------------------------------------------- |
| API              | http://localhost:8080                          |
| RabbitMQ console | http://localhost:15672 (filecosystem / filecosystem) |
| MongoDB          | localhost:27017 (filecosystem / filecosystem, authSource=admin) |

Object storage is Amazon S3. Leave `S3_ENDPOINT` empty so the SDK uses the
regional AWS endpoint. The bucket must already exist; the API only checks it
on startup.

## HTTP API

| Method | Path                          | Purpose                                          |
| ------ | ----------------------------- | ------------------------------------------------ |
| GET    | `/healthz`                    | Liveness                                          |
| GET    | `/readyz`                     | Readiness, including MongoDB and broker           |
| GET    | `/api/v1/capabilities`        | Supported families, operations and upload limits  |
| POST   | `/api/v1/files`               | Multipart upload, field name `file`               |
| GET    | `/api/v1/files/{id}`          | File metadata                                     |
| GET    | `/api/v1/files/{id}/preview`  | Signed URL for the original                       |
| GET    | `/api/v1/files/{id}/jobs`     | Every job created from a file                     |
| POST   | `/api/v1/image/jobs`          | Queue an image job                                |
| GET    | `/api/v1/jobs/{id}`           | Job status plus signed result URLs                |
| GET    | `/api/v1/jobs/{id}/download`  | Redirect to the signed result URL                 |

Creating a job:

```bash
curl -s -F file=@photo.jpg http://localhost:8080/api/v1/files
curl -s http://localhost:8080/api/v1/image/jobs \
  -H 'Content-Type: application/json' \
  -d '{"file_id":"<id>","operation":"convert","params":{"format":"webp","quality":80}}'
```

Operations and their parameters:

| Operation  | Required                | Optional                                     |
| ---------- | ----------------------- | -------------------------------------------- |
| `convert`  | `format`                | `quality`, `strip_metadata`                   |
| `resize`   | `width` and/or `height` | `fit`, `format`, `quality`, `strip_metadata`  |
| `compress` | –                       | `quality`, `format`                           |

`fit` is one of `contain`, `cover` or `fill`; `cover` and `fill` need both
dimensions. Output formats are `jpeg`, `png` and `webp`.

## Layout

```
cmd/api            entrypoint and wiring
internal/config    environment configuration
internal/contracts queue message schema, mirrored in filecosystem-workers
internal/db        MongoDB client and indexes
internal/domain    core types and error sentinels
internal/httpapi   router, middleware and handlers
internal/queue     RabbitMQ client with reconnect
internal/repository MongoDB access
internal/service   upload, image job and result-event logic
internal/storage   Amazon S3 object storage
```

`internal/contracts/contracts.go` is duplicated byte-for-byte in the workers
repository. Change both together or the queue protocol breaks.

Collections: `files` and `jobs`. Indexes are created on boot (`expires_at`,
`file_id`, `status`).

## Adding a new file family

1. Add the kind and its operations to `internal/contracts`.
2. Add validation and a service under `internal/service`.
3. Mount a namespace such as `/api/v1/video` in `internal/httpapi/server.go`.
4. Declare the queue and binding in `internal/queue/queue.go`.
5. Advertise it in `internal/service/capabilities.go`.
