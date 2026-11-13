# Deployment

UniFinSG ships as one backend container (Go API) and a static frontend bundle.
This directory holds the scripts for local Docker Compose and for the reference
AWS deployment in `ap-southeast-1`.

## Local (Docker Compose)

```bash
python3 deploy/init-local.py                          # create .env with random secrets
docker compose -f docker-compose.ops.yml up --build -d
curl http://localhost:8090/healthz
curl http://localhost:8090/readyz
```

`init-local.py` copies `.env.example` to `.env`, fills in random MySQL and Redis
passwords, a 32-byte encryption key and an admin token, prints no secret values,
and refuses to overwrite an existing `.env`. MySQL and Redis bind to
`127.0.0.1` only. Stop with `docker compose -f docker-compose.ops.yml down`
(without `-v` the data volumes are kept).

## AWS reference architecture

| Component | Service |
| --- | --- |
| Backend | ECS Fargate behind an internal Application Load Balancer |
| Database | RDS MySQL |
| Cache / rate limits | ElastiCache Redis (TLS) |
| Frontend | Private S3 bucket behind CloudFront (Origin Access Control); `/api/*` goes to the ALB through a CloudFront VPC origin |
| Uploads | Separate private S3 bucket, lifecycle expiry after 1 day |
| Sign-in | Cognito user pool with hosted UI |
| Secrets | Secrets Manager (database, Redis, encryption key, platform model key) |
| Logs | CloudWatch Logs |
| Images | ECR (immutable tags, scan on push), pushed from GitHub Actions through OIDC |

## Files

| File | Purpose |
| --- | --- |
| `cloud-bootstrap.json` | CloudFormation: ECR repository, GitHub OIDC provider and the image-build role |
| `build-cloud-template.py` | Renders `cloud-core.json` (network, RDS, ElastiCache, ALB, ECS cluster, S3, CloudFront, Cognito, secrets). Makes no AWS calls |
| `cloud-core.json` | Generated CloudFormation template for the core stack |
| `cloudfront-spa.js` | CloudFront function that rewrites SPA routes on the S3 origin |
| `first-deploy.py` | First-deploy steps: `sync-secrets`, `configure-model`, `model-check`, `init-db`, `service-template`. Secrets are passed through private temporary files, never argv |
| `task-definition.example.json` | ECS task definition template; replace every `REPLACE` field |
| `release.sh` | Build, push and roll out a release to existing infrastructure |
| `rollback.sh` | Roll the ECS service back to a previous task definition |
| `smoke.py` | Read-only HTTPS smoke checks against a deployed URL |

The scripts check the AWS account ID and use the `unifinsg` CLI profile by
default; change these for your own account.

## First deployment

1. Deploy `cloud-bootstrap.json`, then build and push a backend image.
2. Run `python3 deploy/build-cloud-template.py` and deploy `cloud-core.json`.
3. Run `first-deploy.py sync-secrets` and `configure-model` to fill Secrets
   Manager, then `init-db --image <image@sha256:digest>` to create the
   application's MySQL account (via `cmd/cloud-init`).
4. Run `first-deploy.py service-template --image ...` to create the ECS service.
   Check `/healthz` and `/readyz`, then upload the frontend.
5. Register the exact Cognito callback (`https://<domain>/api/auth/callback`) and
   sign-out URLs.
6. Run `python3 deploy/smoke.py https://<domain>`.

## Configuration

Besides the database, Redis and Cognito settings, the task definition needs:

| Variable | Where | Value |
| --- | --- | --- |
| `GIN_MODE` | environment | `release` (HTTPS cookies) |
| `LLM_KIND`, `LLM_BASE_URL`, `LLM_MODEL` | environment | Platform model; must support tool calling |
| `LLM_API_KEY` | secret | Platform model key |
| `UNICHATGO_APIKEY_KEY` | secret | 32-byte key that encrypts BYOK keys (required) |
| `LLM_ALLOW_INSECURE_BYOK` | environment | `false` in production |
| `ASSISTANT_RPM`, `CREDITS_*`, `AGENT_*` | environment | Rate limit, credits and agent limits (defaults `5`, `100`/`1`/`2`/`5`, `6`, `30`) |
| `STORAGE_DRIVER` | environment | `s3` |
| `S3_BUCKET`, `S3_REGION`, `S3_PREFIX` | environment | Upload bucket, region, `uploads/` |
| `UPLOAD_TTL_HOURS` | environment | `24` or less; must not outlive the bucket lifecycle rule |
| `TRUSTED_PROXY_CIDRS` | environment | ALB subnets, so client IPs come from trusted forwarding headers |

With `STORAGE_DRIVER=s3`, startup fails if `S3_BUCKET` or `S3_REGION` is missing.

## Private upload bucket

Uploaded images must not share the frontend bucket.

1. Create a separate private bucket with all four **Block Public Access**
   settings on and no static website hosting.
2. Use SSE-S3 default encryption.
3. Add a lifecycle rule: prefix `uploads/`, expire objects 1 day after creation.
   The application's hourly cleanup only updates metadata; S3 objects are removed
   by this rule.
4. Grant the task role only this, scoped to the prefix:

```json
{
  "Effect": "Allow",
  "Action": ["s3:PutObject", "s3:GetObject", "s3:DeleteObject"],
  "Resource": "arn:aws:s3:::<upload-bucket>/uploads/*"
}
```

No public or presigned URLs are generated. Images are served only through
`GET /api/users/:id/agent/uploads/:upload_id`, which checks ownership on every
request. Object keys look like `uploads/<user_id>/<yyyymmdd>/<uuid>.<ext>` and
are never logged.

## CI/CD

- `.github/workflows/ops-ci.yml` runs backend tests (MySQL 8.4, Redis 7.2),
  frontend tests and build, and a Docker image build on pull requests and
  pushes. A `deploy` job runs `release.sh` on `feat_test` / `main` only when the
  repository variable `DEPLOY_ENABLED=true`.
- `.github/workflows/cloud-image.yml` builds the frontend bundle and pushes an
  immutable backend image to ECR through OIDC.

Each GitHub Environment used for deployment needs these variables:
`AWS_DEPLOY_ROLE_ARN`, `ECR_REPOSITORY`, `ECS_CLUSTER`, `ECS_SERVICE`,
`FRONTEND_BUCKET`, `CLOUDFRONT_DISTRIBUTION_ID`, `PUBLIC_URL` (HTTPS, no
trailing slash). Restrict the OIDC role's trust policy to this repository and
environment.

## Release and rollback

`release.sh` (Linux/macOS or Git Bash; needs `aws`, `docker`, `node`, `npm`,
`jq`, `curl`, `tar`) requires `EXPECTED_ACCOUNT_ID`, `AWS_REGION`,
`ECR_REPOSITORY`, `ECS_CLUSTER`, `ECS_SERVICE`, `FRONTEND_BUCKET`,
`CLOUDFRONT_DISTRIBUTION_ID`, `PUBLIC_URL` and `RELEASE_ID`. It:

1. builds the frontend and the backend image, and pushes the image;
2. registers a new task definition pinned to the image digest and updates the
   service with the deployment circuit breaker and automatic rollback;
3. fails if ECS rolled back, then checks `PUBLIC_URL/api/readyz`;
4. uploads static assets first and `index.html` last, keeps old hashed assets,
   invalidates CloudFront, and saves release artifacts in `deploy/output/`.

Backend rollback: set `ROLLBACK_TASK_DEFINITION` from the previous release's
`previous-task-definition.txt` and run `bash deploy/rollback.sh`. Frontend
rollback: re-upload the previous `frontend.tar.gz` (assets first, then
`index.html`) and invalidate CloudFront.

Migrations only move forward. Before rolling back, confirm that the older image
works with the current schema; down migrations are never run automatically.

## Operations notes

- Logs must never contain credentials, user notes, full conversations or upload
  object keys.
- To pause a deployment, set the ECS service's desired count to 0. RDS,
  ElastiCache, the ALB and networking are billed separately and need their own
  handling.
- Never run load tests or enable `AUTH_MODE=local` against production; see
  [`loadtest/README.md`](../loadtest/README.md).
