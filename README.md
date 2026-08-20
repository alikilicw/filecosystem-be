# filecosystem-be

The Filecosystem API: a Go monolith that accepts uploads, queues transformation
jobs and hands back results. Heavy lifting happens in
[`filecosystem-workers`](../filecosystem-workers); the two talk over RabbitMQ.

```
browser ──────────bytes─────────────> S3
   │                                   ^
   │ metadata                          │ fetch
   v                                   │
  api ──job.image─────> rabbitmq ──> worker
   ^                                   │
   └──────────── job.result ───────────┘
```

File bytes never pass through the API. It signs an upload URL, the browser PUTs
straight to S3, and the client reports back that it did. Nothing inspects what
actually landed there, so a file's recorded type and size stay the client's
word.

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

Because browsers upload to the bucket directly, the bucket has to allow it:

```bash
make s3-cors           # edit deploy/s3-cors.json first if your origin differs
```

Without that, uploads fail in the browser with an opaque network error while
`curl` still works, since `curl` sends no preflight.

## Uploads

An upload is a three-step handshake. The API never receives file bytes, and never
learns anything about them beyond what the client declared.

1. `POST /api/v1/files` with the file's name, type and size. The response holds
   the reserved file (`status: pending`), an `upload_url` and its
   `upload_expires_at`.
2. `PUT` the raw file bytes to `upload_url` with `Content-Type` set to the
   reserved file's `mime_type`. The signature pins the key and that content
   type, so the upload can go nowhere else, but neither the bucket nor the API
   checks the size or what the bytes actually are.
3. `POST /api/v1/files/{id}/complete`. This is the only signal the API gets that
   an object appeared, and it is what makes the file usable. The response comes
   back `ready`. Calling it more than once is safe.

| Status    | Meaning                                          |
| --------- | ------------------------------------------------ |
| `pending` | URL issued, waiting for the client to upload      |
| `ready`   | Client reported the upload; usable as a job source |

Only a `ready` file may be used as a job source. Everything sent in step 1 is an
unverified claim that is recorded as-is, so a mislabelled or oversized upload is
only caught when a worker fails to decode it and the job comes back `failed`.

Upload URLs nobody used, and uploads nobody reported, are swept together with
their objects one `UPLOAD_TTL` after they last changed.

## HTTP API

| Method | Path                             | Purpose                                          |
| ------ | -------------------------------- | ------------------------------------------------ |
| GET    | `/healthz`                       | Liveness                                          |
| GET    | `/readyz`                        | Readiness, including MongoDB and broker           |
| GET    | `/api/v1/capabilities`           | Supported families, operations and upload limits  |
| POST   | `/api/v1/files`                  | Reserve a file and get a presigned upload URL     |
| POST   | `/api/v1/files/{id}/complete`    | Report the upload finished, making the file ready |
| GET    | `/api/v1/files/{id}`             | File metadata as declared at upload time          |
| GET    | `/api/v1/files/{id}/preview`     | Signed URL for the original, once uploaded        |
| GET    | `/api/v1/files/{id}/jobs`        | Every job created from a file                     |
| POST   | `/api/v1/image/jobs`             | Queue an image job                                |
| GET    | `/api/v1/jobs/{id}`              | Job status plus signed result URLs                |
| GET    | `/api/v1/jobs/{id}/download`     | Redirect to the signed result URL                 |

Uploading and then converting a file:

```bash
# 1. reserve it and get an upload url
curl -s http://localhost:8080/api/v1/files \
  -H 'Content-Type: application/json' \
  -d '{"filename":"photo.jpg","content_type":"image/jpeg","size_bytes":184320}'

# 2. send the bytes straight to S3
curl -s -X PUT '<upload_url>' \
  -H 'Content-Type: image/jpeg' \
  --data-binary @photo.jpg

# 3. report the upload; the file comes back ready
curl -s -X POST http://localhost:8080/api/v1/files/<id>/complete

# 4. queue work on it
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
internal/service   upload URLs, image jobs and inbound worker events
internal/storage   Amazon S3 object storage
deploy             bucket CORS policy for browser uploads
```

`internal/contracts/contracts.go` is duplicated byte-for-byte in the workers
repository. Change both together or the queue protocol breaks. It also holds the
accepted-upload table the API screens declared types against.

Collections: `files` and `jobs`. Indexes are created on boot (`expires_at`,
`status` + `updated_at`, `file_id`, `status`).

## Adding a new file family

1. Add the kind and its operations to `internal/contracts`, and its accepted
   input types to the `inputMimes` table there.
2. Add a service under `internal/service`.
3. Mount a namespace such as `/api/v1/video` in `internal/httpapi/server.go`.
4. Declare the queue and binding in `internal/queue/queue.go`.
5. Advertise it in `internal/service/capabilities.go`.
