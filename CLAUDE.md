# task-management-microservices

A task management API mid-way through a Strangler Fig decomposition: a Go/Gin
monolith is being carved into services behind an API gateway. Postgres for
storage, Docker Compose for local orchestration.

## Layout: six separate Go modules

There is **no root Go module**, so `go test ./...` from the repo root does
nothing. Every Go command must be run from inside a service directory.

| Directory          | Module path                                    |
|--------------------|------------------------------------------------|
| `monolith/`        | `github.com/P4rz1val22/task-management-api`    |
| `auth-service/`    | `task-management-auth-service`                 |
| `project-service/` | `task-management-project-service`              |
| `task-service/`    | `task-management-task-service`                 |
| `gateway/`         | `task-management-gateway`                      |
| `notification-service/` | `task-management-notification-service`    |

To test everything:

```bash
for m in monolith auth-service project-service task-service gateway \
         notification-service; do
  (cd "$m" && go test ./...)
done
```

`monolith/` is the **old** side of the migration. Don't add new capability to
it; extract from it.

## Running the stack

```bash
docker compose up --build -d     # cold build across 5 modules takes minutes
docker compose ps                # 7 read (healthy); notification-service has
                                 # no healthcheck and just reads "Up"
```

Eight containers, all named `task-mgmt-*`:

| Service           | Container             | Host port |
|-------------------|-----------------------|-----------|
| `postgres`        | `task-mgmt-postgres`  | 5432      |
| `monolith`        | `task-mgmt-monolith`  | 8080      |
| `gateway`         | `task-mgmt-gateway`   | 8081      |
| `auth-service`    | `task-mgmt-auth`      | 8082      |
| `project-service` | `task-mgmt-projects`  | 8083      |
| `task-service`    | `task-mgmt-tasks`     | 8084      |
| `kafka`           | `task-mgmt-kafka`     | 9092      |
| `notification-service` | `task-mgmt-notifications` | *(none)* |

Postgres: user `postgres`, password `password123`, database `taskmanagement`.

```bash
docker compose exec postgres psql -U postgres -d taskmanagement -c '\dt'
```

## Health endpoints — two traps

`notification-service` owns one table, `processed_events`, keyed on `event_id`.
It is how a redelivered event is recognised and not emailed twice; Kafka is
at-least-once, so redelivery is normal rather than exceptional. This service
migrates that table itself, which does **not** join the four-way AutoMigrate
race below — that race is four services fighting over shared tables, this is one
service creating its own.

`notification-service` is the exception to everything in this section: it serves
no HTTP at all, has no port and no healthcheck, so `docker compose ps` shows it
as plain `Up` rather than `(healthy)`. That is correct, not a fault. To check it
is alive, read its log prefix `[NOTIFICATION-SERVICE]` or its committed offsets:

```bash
docker compose exec kafka /opt/kafka/bin/kafka-consumer-groups.sh \
  --describe --group notification-service --bootstrap-server localhost:9092
```

Each other service serves `GET /health` on its own port. **The gateway does not.** It
serves only `GET /gateway/health`. A request to `localhost:8081/health` falls
through `r.NoRoute(proxy.SmartProxy())` and is answered by the *monolith*, so it
returns a healthy-looking 200 that says nothing about the gateway.

`/gateway/health` also returns HTTP 200 even when every dependency is
unreachable, and its `gateway_status` field is a hardcoded literal that can
never report a problem. Assert on the four `*_status` strings:

```bash
curl -s localhost:8081/gateway/health | jq '{monolith_status, auth_service_status,
  project_service_status, task_service_status}'
```

## Kafka

Topic `task-events`, 3 partitions, keyed by task ID, plus `task-events.dlq`
holding messages `notification-service` could not process. A dead-lettered
message keeps its original key and value byte for byte so it can be replayed;
everything diagnostic rides in headers (`dlq-error`, `dlq-topic`,
`dlq-partition`, `dlq-offset`, `dlq-at`).

```bash
docker compose exec kafka /opt/kafka/bin/kafka-console-consumer.sh \
  --topic task-events.dlq --from-beginning --property print.headers=true \
  --bootstrap-server localhost:9092
```

Two listeners:

- Containers on the compose network use **`kafka:29092`**
- Anything on the host (CLI tools, integration tests) uses **`localhost:9092`**

Auto-create is on with `KAFKA_NUM_PARTITIONS: 3`, so an accidentally
auto-created topic still gets the right partition count.

```bash
docker compose exec kafka /opt/kafka/bin/kafka-topics.sh \
  --describe --topic task-events --bootstrap-server localhost:9092
```

## Build constraint: no cgo

Every Dockerfile builds `CGO_ENABLED=0` on Alpine. **Do not add a dependency
that requires cgo** — it means rewriting every Dockerfile and fighting musl.
This rules out `confluent-kafka-go` (use `segmentio/kafka-go`) and
`gorm.io/driver/sqlite` (use `go-sqlmock` for tests).

## Testing

Go tests live in `task-service/internal/events`, `task-service/internal/handlers`
`notification-service/internal/consumer`, `.../internal/services`,
`.../internal/users` and `.../internal/dedupe`. API-level
coverage lives in a Postman collection, `Task Management Microservices API`
(17 requests, 15 assertions).

The collection is a **cloud object** in Postman, not a file in this repo —
Postman 11.x is the web app in an Electron shell. Exports live in
`~/Downloads/`. It is **not idempotent**: re-running against a non-fresh
database fails three ways, none of them application bugs.

1. `Register User` → 409, hardcoded email already exists (harmless; `Login`
   re-sets the token immediately after)
2. `Create Project` / `Update Project` → 409 on hardcoded names, which leaves
   `project_id` empty and cascades into ~5 downstream failures
3. `Filter Tasks by Status` → the test queries `?status=Done` then asserts the
   results are `In Progress`. The test contradicts itself; the endpoint is fine.

For a clean run: `docker compose down -v && docker compose up -d` first. Run
headlessly with `npx newman run <export.json>` (newman is not installed
globally; `npx` works).

Integration tests that need a real broker go behind `//go:build integration`
and run with `go test -tags=integration ./...`, so the default `go test` stays
fast and needs no Docker.

## Outbox

`task-service` does not publish from its handlers. A task change and its event are
written in **one database transaction** — the task row and a row in `outbox` — and a
background poller inside `task-service` publishes from that table every second and
stamps `sent_at`. A published row is kept for 24h and then reaped.

This is why creating a task works, and stays fast, with the broker stopped: the
request path never contacts Kafka. To see pending events as data rather than as log
lines:

```bash
docker compose exec postgres psql -U postgres -d taskmanagement \
  -c 'select id, event_id, key, sent_at, attempts from outbox order by id;'
```

The poller publishes in `id` order and **stops the batch at the first failure**, so a
later event for a task can never overtake an earlier one. Ordering assumes a single
poller, which is what runs. A crash between publishing and stamping `sent_at`
republishes the event; `notification-service` deduplicates on `event_id`, which is why
that had to be built first.

## Known issues, deliberately unfixed

**Concurrent AutoMigrate race.** All four Go services call GORM `AutoMigrate`
the instant Postgres reports healthy, and two race to `CREATE TYPE`. The loser
hits `log.Fatal` and exits; `restart: unless-stopped` recovers it on the next
attempt. **A container flapping once on a first boot against a fresh database
is expected, not a fault.** The real fix is a single migration owner (the
monolith created these tables; the other three don't need to migrate at all).

**~52MB of committed stale binaries** get pulled into every build context,
since there is no `.dockerignore`: `monolith/out`,
`task-service/task-management-task-service`, `gateway/task-management-gateway`.

**Expected log noise, not errors.** Every container logs
`Warning: .env file not found` — `godotenv` looks for `../.env`, which isn't in
the images. Compose supplies the real variables. The root `.env` (gitignored,
holds `DATABASE_URL`, `JWT_SECRET`, `GIN_MODE`) is only used when running a
service on the host from inside its own directory.

## Environment note

`~/Code` is a symlink to `~/Documents/Github/Code`, so `~/Code/task-management-microservices`
and `~/Documents/Github/Code/task-management-microservices` are the same repo.
Docker reports paths via the symlink. There is no second copy.

## Subagents

Default to direct tool calls. Spawn a subagent only when one of these is true:

- The search is genuinely broad — you cannot name the files in advance, and
  answering will mean reading more than roughly ten of them.
- It is a factual question about something outside this repo (a library's
  current API, Claude Code's own behaviour) where answering from memory risks
  a confident wrong answer.

Do not spawn one to read a handful of known files, to re-verify something
already established in the conversation, or to run a command you could run
yourself. An Explore agent once cost 38k tokens and 2.5 minutes here to read
four files that three targeted reads would have covered.

When you do spawn one, state in a single line what you are delegating and why,
before the call. Token usage is never reported back to the main session, so
that line is the only cost signal the user gets.

## Current work

`docs/EVENT_SYSTEM_PLAN.md` is the active plan: finishing the migration with a
Kafka event backbone so notification emails move out of the request path. Ten
stages, each independently committable. **Every stage that produces Go code
writes its failing test first.** Read that document before starting a stage.
