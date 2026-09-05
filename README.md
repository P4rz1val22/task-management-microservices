# Task Management Microservices

> Migration from a monolithic API to microservices using the Strangler Fig
> pattern, with a Kafka event backbone.

## 🏗️ Architecture Overview

A Go/Gin monolith is being carved into services behind an API gateway. The
monolith is the **old** side of the migration and still serves whatever has not
been extracted yet; new capability goes into the services, not into it.

Six Go modules run as eight containers. Requests arrive at the gateway, which
routes by URL prefix. Task changes additionally emit events onto Kafka, where a
consumer turns them into notification emails outside the request path.

```
                             ┌─────────────┐
   client ──── HTTP ───────▶ │   gateway   │ :8081
                             └──────┬──────┘
                                    │ routes by URL prefix
            ┌───────────────┬───────┴───────┬───────────────┐
        /auth/*       /projects/*       /tasks/*      (catch-all)
            ▼               ▼               ▼               ▼
     ┌────────────┐  ┌────────────┐  ┌────────────┐  ┌────────────┐
     │    auth    │  │  project   │  │    task    │  │  monolith  │
     │   :8082    │  │   :8083    │  │   :8084    │  │   :8080    │
     └────────────┘  └────────────┘  └──────┬─────┘  └────────────┘
                                            │
              task row + event row in ONE transaction
                                            ▼
                                     ┌─────────────┐
                                     │   outbox    │  (Postgres table)
                                     └──────┬──────┘
                                            │ poller, 1s tick, in id order
                                            ▼
                                   ┌──────────────────┐
                                   │   task-events    │  3 partitions,
                                   │      (Kafka)     │  keyed by task ID
                                   └────────┬─────────┘
                                            │
                                            ▼
                              ┌──────────────────────────┐
                              │  notification-service    │ ──▶ email (SMTP)
                              │  consumer group          │
                              └─────────────┬────────────┘
                                            │ unprocessable message
                                            ▼
                                  ┌────────────────────┐
                                  │  task-events.dlq   │
                                  └────────────────────┘
```

**Why the broker is here.** It is bought for decoupling and failure isolation,
not for throughput this system will never see. Before it, a task write waited on
mail delivery, so a slow or broken mail path degraded task creation. Now a task
write commits and returns; notification happens afterwards, and a broker outage
delays emails rather than failing writes.

**What it deliberately does not buy.** Not exactly-once delivery. The outbox
poller publishes a row and then marks it sent, so a crash in between republishes
the event — which is why the consumer deduplicates on `event_id`. Delivery is
at-least-once, and redelivery is normal rather than exceptional.

## 🚀 Quick Start

### Prerequisites
- Docker and Docker Compose
- Go 1.24.5+ (only to run tests or a service on the host)

### Run the whole stack
```bash
make up            # docker compose up --build -d
docker compose ps  # 7 read (healthy); notification-service reads "Up"
```

A cold build across five modules takes minutes. Eight containers, all named
`task-mgmt-*`:

| Service                 | Container                 | Host port |
|-------------------------|---------------------------|-----------|
| `postgres`              | `task-mgmt-postgres`      | 5432      |
| `monolith`              | `task-mgmt-monolith`      | 8080      |
| `gateway`               | `task-mgmt-gateway`       | 8081      |
| `auth-service`          | `task-mgmt-auth`          | 8082      |
| `project-service`       | `task-mgmt-projects`      | 8083      |
| `task-service`          | `task-mgmt-tasks`         | 8084      |
| `kafka`                 | `task-mgmt-kafka`         | 9092      |
| `notification-service`  | `task-mgmt-notifications` | *(none)*  |

### Verify everything works

```bash
curl -s localhost:8081/gateway/health | jq '{monolith_status, auth_service_status,
  project_service_status, task_service_status}'
```

Two traps worth knowing before you trust a health check:

- **The gateway does not serve `/health`** — only `/gateway/health`. A request to
  `localhost:8081/health` falls through the gateway's catch-all route and is
  answered by the *monolith*, so it returns a healthy-looking 200 that says
  nothing about the gateway.
- **`/gateway/health` returns HTTP 200 even when every dependency is
  unreachable**, and its `gateway_status` field is a hardcoded literal that can
  never report a problem. The four downstream `*_status` strings above are the
  only part of the response that carries information.

`notification-service` serves no HTTP at all, so it has no port and no
healthcheck and shows as plain `Up` rather than `(healthy)`. That is correct, not
a fault. To check it is alive, read its `[NOTIFICATION-SERVICE]` log prefix or
its committed offsets:

```bash
docker compose exec kafka /opt/kafka/bin/kafka-consumer-groups.sh \
  --describe --group notification-service --bootstrap-server localhost:9092
```

### Running a single service on the host
```bash
cd task-service && go run main.go   # reads ../.env
```

## 📁 Repository Structure

**Six separate Go modules, and no root Go module.** `go test ./...` from the
repository root matches nothing and exits 0, which is worse than failing. Every
Go command must run from inside a service directory — which is what the
`Makefile` targets do.

| Directory                | Module path                                 |
|--------------------------|---------------------------------------------|
| `monolith/`              | `github.com/P4rz1val22/task-management-api` |
| `auth-service/`          | `task-management-auth-service`              |
| `project-service/`       | `task-management-project-service`           |
| `task-service/`          | `task-management-task-service`              |
| `gateway/`               | `task-management-gateway`                   |
| `notification-service/`  | `task-management-notification-service`      |

```
task-management-microservices/
├── README.md                      # This file
├── Makefile                       # test / test-integration / test-api / up
├── docker-compose.yml             # Eight-container orchestration
├── docs/
│   ├── EVENT_SYSTEM_PLAN.md       # Design and staged plan for the event backbone
│   └── postman/                   # API-level test collection (committed export)
├── gateway/                       # API Gateway (8081)
│   ├── internal/proxy/            # Request routing
│   └── internal/e2e/              # Integration tests (build tag)
├── auth-service/                  # Authentication (8082)
├── project-service/               # Projects (8083)
├── task-service/                  # Tasks (8084)
│   ├── internal/events/           # Event contract + Kafka publisher
│   ├── internal/outbox/           # Transactional outbox + poller
│   ├── internal/handlers/         # Task CRUD + filtering
│   └── internal/e2e/              # Integration tests (build tag)
├── notification-service/          # Kafka consumer, no HTTP
│   ├── internal/consumer/         # Consumer loop + dead-letter routing
│   ├── internal/dedupe/           # processed_events, keyed on event_id
│   ├── internal/services/         # Email rendering + SMTP
│   └── internal/users/            # Resolves actor_id to an address
└── monolith/                      # Original monolithic API (8080)
    ├── cmd/server/
    └── internal/
```

### Build constraint: no cgo

Every Dockerfile builds `CGO_ENABLED=0` on Alpine, so **a dependency that
requires cgo would mean rewriting every Dockerfile and fighting musl.** That
rules out `confluent-kafka-go` (hence `segmentio/kafka-go`) and
`gorm.io/driver/sqlite` (hence `go-sqlmock` for tests).

## 🔧 Services Overview

### 1. API Gateway (Port 8081)
**Responsibility**: Intelligent request routing and service orchestration

**Key Features**:
- URL-based routing (`/auth/*`, `/projects/*`, `/tasks/*`), with everything
  unmatched falling through to the monolith
- Health aggregation at `/gateway/health` — **note it does not serve `/health`**
- Request/response logging
- Error handling and fallback strategies

**Technology**: Go + Gin + Reverse Proxy

### 2. Auth Service (Port 8082)
**Responsibility**: User authentication and JWT token management

**Endpoints**:
- `POST /auth/register` - User registration
- `POST /auth/login` - User authentication
- `GET /health` - Service health check

**Key Features**:
- JWT token generation (24-hour expiration)
- Password hashing with bcrypt
- User registration with duplicate checking

### 3. Project Service (Port 8083)
**Responsibility**: Project management and ownership

**Endpoints**:
- `GET /projects` - List user's projects
- `POST /projects` - Create new project
- `GET /projects/:id` - Get project details with task count
- `PUT /projects/:id` - Update project
- `DELETE /projects/:id` - Delete project (if no tasks exist)

**Key Features**:
- Project ownership validation
- Cross-service data enrichment (user names, task counts)
- JWT-based authorization

### 4. Task Service (Port 8084)
**Responsibility**: Task management with advanced filtering

**Endpoints**:
- `GET /tasks` - List and filter tasks
- `POST /tasks` - Create new task
- `GET /tasks/:id` - Get task details
- `PUT /tasks/:id` - Update task
- `DELETE /tasks/:id` - Delete task

**Key Features**:
- Complex filtering (project, status, priority, due dates)
- Cross-service data enrichment (project names, user names)
- Advanced validation (status, priority, estimate)
- Authorization checks (project ownership)
- Emits `task.created` / `task.updated` / `task.deleted` events — written to the
  `outbox` table in the same transaction as the task, never published from the
  handler. See [Event Backbone](#-event-backbone).

**Filter Parameters**:
```bash
GET /tasks?project_id=1&status=In Progress&priority=High&due_date_from=2025-01-01
```

### 5. Monolith (Port 8080)
**Responsibility**: Legacy functionality not yet extracted

**Current Endpoints**:
- `GET /users/me` - User profile management
- `PUT /users/me` - Update user profile
- All other non-auth, non-project, non-task endpoints

### 6. Notification Service (no HTTP port)
**Responsibility**: Turning task events into notification emails, off the
request path.

This service has **no HTTP server, no port and no healthcheck**. It is a Kafka
consumer and nothing else, which is why `docker compose ps` shows it as `Up`
rather than `(healthy)`.

**Key Features**:
- Consumer group `notification-service` on topic `task-events`
- Deduplicates on `event_id` before sending, because delivery is at-least-once
- Routes messages it cannot process to `task-events.dlq` rather than blocking
- Renders and logs the email when SMTP is unconfigured, so the whole pipeline
  can be exercised with nothing leaving the machine

**Owns one table**: `processed_events`, keyed on `event_id`. It migrates that
table itself.

## 📨 Event Backbone

### The contract

Three event types — `task.created`, `task.updated`, `task.deleted` — published
as JSON onto one topic. The envelope carries `event_id`, `event_type`,
`task_id`, `actor_id`, the task payload, and for updates a `changes` map.

The envelope is **self-contained on purpose**: `notification-service` never
calls back into `task-service` for task data. It does resolve `actor_id` to an
email address from the database, because an address is mutable personal data and
freezing it into every event on the topic would be wrong.

### Topics and listeners

`task-events` has **3 partitions and is keyed by task ID**, so every event for
one task lands on one partition and therefore stays in order relative to itself.
A `task.updated` can never overtake its own `task.created`.

`task-events.dlq` holds messages the consumer could not process. A dead-lettered
message keeps its original key and value byte for byte so it can be replayed;
everything diagnostic rides in headers (`dlq-error`, `dlq-topic`,
`dlq-partition`, `dlq-offset`, `dlq-at`).

Two listeners, and picking the wrong one is the most common mistake here:

- Containers on the compose network use **`kafka:29092`**
- Anything on the host — CLI tools, integration tests — uses **`localhost:9092`**

```bash
# Inspect the topic
docker compose exec kafka /opt/kafka/bin/kafka-topics.sh \
  --describe --topic task-events --bootstrap-server localhost:9092

# Read the dead-letter topic with its diagnostic headers
docker compose exec kafka /opt/kafka/bin/kafka-console-consumer.sh \
  --topic task-events.dlq --from-beginning --property print.headers=true \
  --bootstrap-server localhost:9092
```

### The transactional outbox

`task-service` **does not publish from its handlers.** A task change and its
event are written in one database transaction — the task row and a row in
`outbox` — and a background poller publishes from that table every second and
stamps `sent_at`. Published rows are kept 24h, then reaped.

This is the difference between an event that might happen and one that will. The
handler used to commit the task and then publish, which is two things that were
not one thing: a crash or an unreachable broker in between left the task
existing and the event never happening, with nothing anywhere that could replay
it. Retrying could not fix that, because the failure can be the process
disappearing, and no code runs after a crash.

Two consequences worth stating:

- **Creating a task works, and stays fast, with the broker stopped.** The
  request path never contacts Kafka.
- **The poller publishes in `id` order and stops the batch at the first
  failure**, so a later event for a task can never overtake an earlier one.
  Ordering assumes a single poller, which is what runs.

To see pending events as data rather than as log lines:

```bash
docker compose exec postgres psql -U postgres -d taskmanagement \
  -c 'select id, event_id, key, sent_at, attempts from outbox order by id;'
```

## 🔒 Authentication Flow

### JWT Token Lifecycle
```
1. Client → Gateway → Auth Service: POST /auth/login
2. Auth Service: Validates credentials, creates JWT
3. Auth Service → Gateway → Client: Returns JWT token
4. Client → Gateway: Subsequent requests with Authorization header
5. Gateway → Target Service: Forwards request with JWT
6. Target Service: Validates JWT independently
7. Target Service → Gateway → Client: Returns authorized response
```

**Security Features**:
- Shared JWT secret across all services
- 24-hour token expiration
- User ID and email in token claims
- Bearer token validation middleware

## 📊 Database

Postgres, one database `taskmanagement`. User `postgres`, password
`password123`.

```bash
docker compose exec postgres psql -U postgres -d taskmanagement -c '\dt'
```

**Tables**:

| Table              | Owner                  | Purpose |
|--------------------|------------------------|---------|
| `users`            | monolith               | Accounts and authentication |
| `projects`         | monolith               | Project information and ownership |
| `tasks`            | monolith               | Task details with project/user relationships |
| `outbox`           | `task-service`         | Events awaiting publication, written in the same transaction as the task |
| `processed_events` | `notification-service` | `event_id`s already handled, so a redelivery is not emailed twice |

## 🧪 Testing

Three tiers, and the split is the point: **the fast tier needs no Docker.** An
undocumented build tag is an invisible test suite, so all three have a `make`
target.

| Tier | Command | Needs Docker | What it covers |
|---|---|---|---|
| Unit | `make test` | no | Event contract, publisher, outbox, handlers, consumer, dedupe — all against a mock database |
| Integration | `make test-integration` | yes, stack up | Real broker, real Postgres, real HTTP through the gateway |
| API | `make test-api` | yes, stack up | The Postman collection, via newman |

`make test-all` runs all three.

### Unit tests

```bash
make test
```

171 tests, no broker and no database. They live in `task-service/internal/`
(`events`, `outbox`, `handlers`) and `notification-service/internal/`
(`consumer`, `services`, `users`, `dedupe`).

**Honest scope:** unit coverage exists for `task-service` and
`notification-service` — the two services this event work touched. `monolith`,
`auth-service` and `project-service` have no Go unit tests. The gateway has no
unit tests either, but is now covered end to end.

Fakes are of narrow interfaces this repo owns (`messageWriter`, `mailSender`),
not of a Kafka broker wholesale. Anything that needs a real broker goes behind
the build tag instead.

### Integration tests

```bash
docker compose up -d
make test-integration
```

Behind `//go:build integration`, so a bare `go test ./...` never runs them and
never needs Docker. Three packages:

- `task-service/internal/events` — publishes to a real broker and reads the
  message back, and proves every event for one task lands on one partition.
- `gateway/internal/e2e` — register, create a project, create a task and read it
  back, all over HTTP through the gateway, asserting the cross-service enrichment
  that only appears if `task-service` reached `project-service`. Plus rejected
  credentials, and a health check that asserts the four downstream `*_status`
  fields rather than the status code.
- `task-service/internal/e2e` — **stops the Kafka container**, creates a task,
  asserts it still returns 201 and that the event is sitting in `outbox` with
  `sent_at` NULL, then starts Kafka and watches the poller drain it unaided. It
  restores the container on cleanup, but do not run it against a stack anyone
  else is using.

**`make test-integration` passes `-p 1`, and that is not cosmetic.** Go runs
separate packages in parallel, and the outbox test stops the broker; without
`-p 1` it pulls the broker out from under the publisher tests in
`internal/events`, which then fail for a reason that has nothing to do with them.

To run one package directly:

```bash
cd task-service && go test -tags=integration -p 1 -timeout 10m ./...
```

Overridable with `GATEWAY_URL`, `KAFKA_BROKERS`, `TEST_DATABASE_URL` and
`REPO_ROOT`. Note the tests read **`TEST_DATABASE_URL`, not `DATABASE_URL`**:
the root `.env` points `DATABASE_URL` at a hosted database, and a test that
silently asserted against that would pass or fail for reasons unrelated to the
code.

### API tests (Postman)

```bash
make test-api
```

`docs/postman/Task Management Microservices API.postman_collection.json` —
17 requests, 17 test scripts, 18 assertions. newman is not installed globally;
`npx` fetches it.

**The collection is idempotent**, so it does not need a fresh database and can
be run repeatedly. It did not used to be: three requests posted hardcoded values
that collided on unique constraints on a second run, which cost 8 of 15
assertions and a cascade of downstream failures. Emails and project names are
now minted per run in pre-request scripts.

The collection lives in Postman as a cloud object; the file here is a committed
export. Re-export after changing it there, or the two drift.

### Manual testing

```bash
# 1. Register and capture a token
TOKEN=$(curl -s -X POST http://localhost:8081/auth/register \
  -H "Content-Type: application/json" \
  -d '{"name":"Tester","email":"me@example.com","password":"password123"}' \
  | jq -r .token)

# 2. Create a project (routed to project-service)
curl -s -X POST http://localhost:8081/projects \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"Test Project","description":"Testing microservices"}'

# 3. Create a task (routed to task-service, which writes task + outbox row)
curl -s -X POST http://localhost:8081/tasks \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"title":"Test Task","project_id":1,"status":"In Progress","priority":"High"}'

# 4. Filter tasks
curl -s -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8081/tasks?status=In Progress&priority=High"
```

Valid enums: `status` is one of `Not Started`, `In Progress`, `Done`, `Blocked`;
`priority` is `Low`, `Medium`, `High`, `Urgent`; `estimate` is `S`, `M`, `L`,
`XL`.

To watch the event actually flow, stop the broker first and see that step 3 still
returns 201 — then start it again and watch the poller drain the row.

## ⚙️ Environment Variables

Compose supplies everything the containers need. The root `.env` (gitignored) is
only read when running a service on the host from inside its own directory.

| Variable | Used by | Notes |
|---|---|---|
| `DATABASE_URL` | all but the gateway | Containers use the compose hostname `postgres`, not `localhost` |
| `JWT_SECRET` | auth, projects, tasks, monolith | Must be the **same** across services or tokens fail to validate downstream |
| `KAFKA_BROKERS` | task-service, notification-service | `kafka:29092` in a container, `localhost:9092` on the host |
| `KAFKA_TOPIC` | task-service, notification-service | `task-events` |
| `KAFKA_GROUP_ID` | notification-service | `notification-service`. The broker keys committed offsets on this exact string, so **changing it re-notifies every task ever created** |
| `SMTP_USERNAME`, `SMTP_PASSWORD` | notification-service | Left empty on purpose: the service then renders each email and logs `would send: <subject> to <address>` instead of delivering it |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_FROM` | notification-service | Default to `smtp.gmail.com:587` and the username |

## 📈 Performance Considerations

### Latency Impact
- **Additional hop**: Client → Gateway → Service adds ~1-2ms
- **Network overhead**: HTTP proxying instead of direct calls
- **Service startup**: Multiple processes vs single monolith

### Scalability Benefits
- **Independent scaling**: Scale services based on demand
- **Resource isolation**: Memory/CPU per service
- **Deployment isolation**: Update services independently


## 🛠️ Technology Stack

**Languages & Frameworks**:
- Go 1.24.5
- Gin HTTP framework
- GORM ORM

**Infrastructure**:
- PostgreSQL
- Apache Kafka (`segmentio/kafka-go` — pure Go, because every image is built
  `CGO_ENABLED=0`)
- JWT authentication
- Docker Compose, multi-stage builds
- Reverse proxy routing

**Testing**:
- `testing` plus table-driven subtests — no assertion framework
- `go-sqlmock` for database interaction, since a cgo-free SQLite driver is not
  available under this build constraint
- Postman/newman at the API boundary
- Swagger API documentation (monolith)

## 🤝 Contributing

### Development Workflow
1. `make up`
2. Make changes to individual services
3. `make test` — the fast loop, no Docker needed
4. `make test-integration` and `make test-api` before committing
5. Update documentation as needed

New capability goes into the services, **not into `monolith/`**. The monolith is
the old side of the migration; extract from it rather than adding to it.

### Adding New Services
1. Create the service directory with its own `go.mod`
2. Follow existing patterns for structure
3. Update API Gateway routing
4. Add a health check endpoint
5. Update `docker-compose.yml`
6. Add the module to `MODULES` in the `Makefile`, or its tests never run
7. Add tests to the Postman collection

## 🐛 Known Issues

Documented rather than hidden, because each one looks like a bug the first time
you see it.

**Concurrent AutoMigrate race.** All four Go services call GORM `AutoMigrate` the
instant Postgres reports healthy, and two race to `CREATE TYPE`. The loser exits;
`restart: unless-stopped` recovers it on the next attempt. **A container flapping
once on a first boot against a fresh database is expected, not a fault.** The
real fix is a single migration owner — the monolith created these tables, and the
other three do not need to migrate at all.

**`Warning: .env file not found` in every container is expected.** `godotenv`
looks for `../.env`, which is not in the images; Compose supplies the real
variables.

**~52MB of committed stale binaries** get pulled into every build context,
because there is no `.dockerignore`: `monolith/out`,
`task-service/task-management-task-service`, `gateway/task-management-gateway`.

## 📖 Documentation

- **Event system design and staged plan**: [`docs/EVENT_SYSTEM_PLAN.md`](docs/EVENT_SYSTEM_PLAN.md)
- **Postman collection**: [`docs/postman/`](docs/postman/)
- **Monolith Swagger docs**: `monolith/docs/`

---

**Built as part of an 8-week intensive coding journey - Week 4: Microservices Architecture**

*A complete evolution from monolithic systems to containerized microservices, showcasing production-ready patterns, Docker orchestration, and enterprise-level architectural thinking. Ready for deployment on any cloud platform.*

**🐳 Docker + 🏗️ Microservices + 🚀 Production Ready**