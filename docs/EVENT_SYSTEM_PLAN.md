# Plan: Finish the Strangler Fig migration with a Kafka event backbone

Working plan for pairing with Claude Code. Each stage is independently runnable,
verifiable, and committable — start a session, do one stage, commit, stop.

**Every stage that produces Go code writes its test first.** The stage's first checkbox is
always "the new test fails, and fails for the right reason." Section 4 explains why that
discipline is load-bearing here and not ceremony.

Clean `main` at `157beff` when this plan was written. Work on a branch:

```bash
git checkout -b feat/kafka-event-system
```

---

## 1. Context: why this exists

This is not "add Kafka to get a keyword." The decomposition in this repo is genuinely
unfinished, and the repo says so itself.

The monolith sends task notification emails from an inline goroutine:

- `monolith/internal/handlers/task.go:121` — `go func()` on task create
- `monolith/internal/handlers/task.go:377` — `go func()` on task update
- `monolith/internal/services/email.go` — a complete `EmailService`: SMTP, HTML
  templates, change diffing via `ChangeDetail`, priority/estimate/status rendering

The extracted `task-service` dropped that capability entirely and left TODOs behind:

- `task-service/internal/handlers/handlers.go:112` — `// TODO: Send notification to
  Notification Service (future microservice)` / `// For now, we skip the email notification`
- `task-service/internal/handlers/handlers.go:322` — `// Track changes for notification
  (future use)` with `originalTitle`/`originalStatus`/`originalPriority`/`originalEstimate`
  commented out
- `task-service/internal/handlers/handlers.go:342` — `// TODO: Send change notification to
  Notification Service`

So the migration traded a working feature for a TODO. This plan builds the notification
service those TODOs point at.

**Why events rather than an HTTP call to the notification service:** if `task-service`
called it directly, an SMTP outage or a down notification service would fail or block task
creation — a write path taking on the availability of a non-critical side effect. Publishing
an event decouples them: `task-service` returns as soon as the task is committed and the
event is durably logged, and the consumer catches up on its own schedule. That is also the
honest answer to the two design questions this work invites in an interview ("why not just
call it?" and "what happens when the consumer is down?").

**What Kafka is explicitly NOT buying here.** There is no throughput or scale problem to
solve — this is a local seven-container stack with no production traffic. Anyone claiming Kafka
here is about volume is overselling it. The justification is decoupling and reasoning about
failure, and that justification is honest.

**What the finished system will provide.** When Stages 1–9 are done, the observable end state
is:

1. **Task writes no longer depend on mail delivery.** Creating a task through the gateway
   returns 201 whether the broker is up, down, or mid-restart. There is a Go test for exactly
   this, and a Postman test that proves it end to end with Kafka stopped.
2. **The dropped feature is restored on the new side of the migration.** Notification emails
   fire again for task creation and updates — including a change summary listing exactly which
   fields changed and nothing else — sent from a `notification-service` rather than from a
   goroutine inside a request handler. All three TODOs are deleted.
3. **A consumer that survives real failure.** It can be stopped, restarted, or crash
   mid-processing without losing events or sending duplicate emails (deduped on `event_id`),
   and one poison message will not wedge the partition (retry, then dead-letter topic).
4. **Test coverage below the HTTP boundary, where there was none.** Go unit tests for the wire
   contract, the partition key, the change-diffing rules, and the "publish failure still
   returns 201" guarantee — complementing the existing 17-request Postman suite rather than
   replacing it.
5. **A gap we can articulate rather than hide — and then closed.** Events were published after
   the DB commit, so a crash between the two lost an event. **Stage 8b fixed it**: the event is
   now written to an `outbox` table inside the same transaction as the task, and a poller
   publishes from there.
   Whether or not it gets built, being able to name it is part of the deliverable.

---

## 2. Two decisions already made, and why

**Kafka in KRaft mode, single container, `apache/kafka:3.9.0`.** No Zookeeper. Modern Kafka
does not need it, and a second container buys nothing here.

**`github.com/segmentio/kafka-go`, NOT `confluent-kafka-go`.** This matters and is easy to
get wrong. Every Dockerfile in this repo builds with `CGO_ENABLED=0` on Alpine (see
`task-service/Dockerfile`). `confluent-kafka-go` wraps the C library `librdkafka` and
requires cgo, so choosing it means rewriting every Dockerfile and fighting Alpine's musl
libc. `segmentio/kafka-go` is pure Go and drops straight into the existing build with no
Dockerfile changes. If a future session suggests switching to `confluent-kafka-go`, this is
the reason not to.

There is a second, test-specific reason to prefer it: `kafka-go`'s writer is an ordinary Go
struct behind an interface you can define yourself, so the publisher is unit-testable with a
hand-written fake and no broker. A cgo client is much harder to fake.

---

## 3. Event contract

One topic, `task-events`, carrying all three event types. Not three topics — a single topic
partitioned by task ID guarantees that events for the same task are consumed in the order
they happened, which is exactly the ordering guarantee that matters here. Three topics would
let a `task.updated` overtake its own `task.created`.

- **Topic:** `task-events`
- **Partition key:** the task ID as a string. Same task, same partition, ordered.
- **Value:** JSON envelope below.

```json
{
  "event_id":   "uuid-v4",
  "event_type": "task.created",
  "occurred_at": "2026-09-04T14:03:11Z",
  "task_id":    42,
  "actor_id":   7,
  "task": {
    "id": 42, "title": "...", "description": "...", "project_id": 3,
    "status": "In Progress", "priority": "High", "estimate": "M",
    "due_date": "2026-09-30T00:00:00Z"
  },
  "changes": [
    { "field": "Status", "from": "Not Started", "to": "In Progress" }
  ]
}
```

Notes on the shape:

- `event_id` exists so the consumer can dedupe. Kafka is at-least-once: a consumer that
  crashes after sending an email but before committing its offset will see that event again
  on restart. Without dedupe, that is a duplicate email.
- `changes` is populated only on `task.updated`, and maps 1:1 onto the existing
  `services.ChangeDetail` struct in `monolith/internal/services/email.go` — deliberately, so
  the email service can be reused with no changes to its signature.
- `actor_id` is the `user_id` the handlers already read via `c.GetUint("user_id")`. The
  consumer needs it to look up the recipient's email.
- The event carries the whole task, so the consumer never has to call back into
  `task-service`. Keep it that way.

This contract is what Stage 2 encodes in a test **before** any producer or consumer exists.
Once two services depend on these exact JSON field names, changing them is a coordinated
deploy; the test is what makes an accidental rename fail loudly instead of silently
delivering `null` to the consumer.

---

## 4. Testing strategy (read before Stage 0)

This project is not untested — it is tested at the HTTP boundary, and the existing suite is
real work. What it has no coverage of is anything below that boundary, which is precisely
where the event system lives.

**What exists today.** A Postman collection, `Task Management Microservices API` (schema
v2.1.0) — 17 requests across 7 folders with 15 `pm.test()` blocks, plus a larger companion
collection for the monolith.

Where it lives matters for Stage 9. Postman 11.x is the web app in an Electron shell (the
desktop app loads `https://desktop.postman.com/` behind a service worker), so collections are
cloud objects tied to the account and workspace, not local files — the entire local IndexedDB
store is ~140KB of session state. The only copy on disk is a manual export at
`~/Downloads/postman_testing_suite_exports/Task Management Microservices API.postman_collection.json`,
dated 2025-08-25, which may already have drifted from the live collection.

The assertions are genuine rather than status-code-only: `Create
Task` checks that the response body carries an `id` and that the echoed title matches what
was sent, and the suite chains `auth_token`, `project_id`, and `task_id` through collection
variables so the folders run in sequence as a real workflow. Match that style in Stage 9.

Two gaps in it worth naming, neither of which this plan fixes: it exercises happy paths only,
so the validation rejections in `task-service` (bad status, priority, estimate) and the
401/404 paths are unasserted; and three of the four direct service-health requests have no
test block at all. Also, the collection lives in `~/Downloads`, not in git, so it is a
snapshot that can silently drift from the code or be lost — Stage 9 commits it into the repo.

**What does not exist** is any `*_test.go` file, in any service. So the Go harness has to be
established alongside the first feature rather than assumed, and there is no existing Go
convention to match — the conventions are set here.

### Why test-first genuinely pays here, not just as discipline

Most of this work is *pure data transformation* — build an envelope from a task, diff an old
task against a new one, decide whether an event is worth publishing. That code has no I/O in
it, takes plain structs in and plain structs out, and is the exact shape where writing the
assertion first is easy and shapes the design. The rule "a no-op update publishes nothing"
is a one-line test and an unenforceable comment; writing the test first is what makes it the
former.

Test-first also forces the seam that the current code lacks. `task-service`'s handlers call
the package-level global `database.DB` directly (`task-service/internal/database/database.go`
declares `var DB *gorm.DB`). If the publisher is added the same way — a second global, called
inline — nothing about it is testable. Writing the handler test first makes you inject a
`Publisher` interface, which is both the testable design and the better one.

### Three layers, and what lives at each

**Layer 1 — pure unit tests. No DB, no broker, no network.** The event envelope, its JSON
serialization, and the change-diffing logic. These run in milliseconds with `go test ./...`
and are the majority of the tests in this plan. Stages 2, 5, 7 are almost entirely here.

**Layer 2 — fake-collaborator tests.** A `Publisher` interface with a hand-written fake that
records what it was asked to publish; a `sender` interface for email. These assert *behavior
at a boundary*: "creating a task publishes exactly one `task.created` whose key is the task
ID," "a publish failure still returns 201." Where a handler test needs the database, use
`github.com/DATA-DOG/go-sqlmock` driving the existing gorm postgres driver:

```go
sqlDB, mock, _ := sqlmock.New()
gdb, _ := gorm.Open(postgres.New(postgres.Config{
    Conn:                 sqlDB,
    PreferSimpleProtocol: true,
}), &gorm.Config{})
database.DB = gdb // restore in t.Cleanup
```

Two gotchas worth knowing before you fight them: gorm's `Create` against Postgres emits
`INSERT ... RETURNING "id"`, so it needs `mock.ExpectQuery(...)` wrapped in `ExpectBegin()`
/ `ExpectCommit()`, not `ExpectExec`. And when the expectation does not match, sqlmock's
error message prints the actual SQL — read it and paste it into the expectation rather than
guessing.

**Do not reach for a SQLite test database instead.** `gorm.io/driver/sqlite` requires cgo,
which is exactly the dependency section 2 chose `kafka-go` to avoid; the pure-Go
`glebarez/sqlite` fork works but silently differs from Postgres on the `RETURNING` behavior
above, which makes the test lie about what production does.

**Layer 3 — integration tests, behind a build tag.** Real broker, real Postgres, via the
Compose stack. Every such file starts with:

```go
//go:build integration
```

and is run explicitly with `go test -tags=integration ./...`. This is the important part:
`go test ./...` must stay fast and require no Docker, so it can run on a plane and in CI
without a service matrix. Integration tests are the only place a real broker appears.

### What is deliberately NOT unit tested

Naming this is as valuable as the tests themselves, because the alternative is fake tests
that mock a broker and assert that the mock was called:

- **Broker configuration** (Stage 1). A KRaft listener config is verified by the broker
  starting and `kafka-topics.sh` succeeding. A Go test asserting the contents of a YAML file
  tests nothing.
- **Consumer-group offset semantics** (Stage 5). That offsets survive a restart and that a
  stopped consumer catches up is a property of Kafka, not of this code. It is verified by
  hand with `docker compose stop` / `start`, and that manual check is the payoff of the whole
  design — see it with your own eyes.
- **Real SMTP delivery** (Stage 6). Tested against a fake sender; that a real mail server
  accepts the message is verified once, manually, with real credentials.

### Harness conventions

Add a `test` and `test-integration` target to the `Makefile` (create one if the repo has
none — it makes the two-tier split discoverable instead of tribal knowledge):

```make
test:
	cd task-service && go test ./... && cd ../notification-service && go test ./...

test-integration:
	cd task-service && go test -tags=integration ./...
```

Each service is a separate Go module, so there is no repo-root `go test ./...` that covers
everything — the `Makefile` is what papers over that.

---

## 5. Stages

Ten stages, 0 through 9. Each Go-code stage follows the same rhythm:

> **RED** — write the test, run it, watch it fail for the reason you predicted.
> **GREEN** — write the smallest implementation that passes.
> **COMMIT** — one commit with both, so the diff shows the test and the code that satisfies it.

Committing the test and implementation together is deliberate: a reviewer (or you, in six
months) can see the assertion next to the behavior. Committing tests separately just to prove
you wrote them first is theater.

---

### Stage 0 — Baseline (no code)

Confirm the stack runs before changing anything, so a later failure is attributable.

**Startup here is racy, so poll before you believe anything.** None of the five Go services
define a healthcheck, and the gateway's `depends_on` has no `condition:` clause
(`docker-compose.yml:114-118`), so the gateway accepts traffic on 8081 while the backends are
still migrating. Worse, all four Go services run GORM `AutoMigrate` against overlapping tables
the moment Postgres reports healthy, which can deadlock into `log.Fatal`. `restart:
unless-stopped` recovers it, so **a container that flaps once on first boot is expected, not
broken** — read its logs and confirm it was the migration race before treating it as a fault.

```bash
docker compose up --build -d          # cold build across 5 Go modules: budget several minutes
docker compose ps                     # expect 6 containers; check the RESTARTS column
docker compose logs postgres | tail   # confirm pg_isready passed
```

Then health-check each service directly, and the gateway through its aggregate endpoint:

```bash
for p in 8080 8082 8083 8084; do curl -s "localhost:$p/health" | jq -c; done
curl -s localhost:8081/gateway/health | jq '{monolith_status, auth_service_status,
  project_service_status, task_service_status}'
```

Two traps in that last one. **There is no `/health` on the gateway** — only
`/gateway/health` (`gateway/main.go:32`). A request to `localhost:8081/health` falls through
`r.NoRoute(proxy.SmartProxy())` and gets answered by the *monolith*, so it returns a cheerful
200 that says nothing about the gateway. And `/gateway/health` returns HTTP 200 even when every
dependency is unreachable, while `gateway_status` is a hardcoded literal
(`gateway/internal/proxy/proxy.go:225-245`). Assert on the four `*_status` strings; ignore the
status code and ignore `gateway_status`.

Then run the existing API suite, which is a far better baseline than curling by hand:

```bash
npx newman run ~/Downloads/postman_testing_suite_exports/"Task Management Microservices API.postman_collection.json"
```

`gateway_url` defaults to `http://localhost:8081` inside the collection, so this needs no
configuration.

**Baseline measured 2026-09-04.** On a *fresh* database: 17 requests, 0 request failures,
15 assertions, 1 failure — and it was a real pre-existing bug, not an environment artifact.

`Filter Tasks by Status` failed with `TypeError: Cannot read properties of null (reading
'forEach')`, because `var taskList []gin.H` is a nil slice and Go marshals nil slices to JSON
`null` rather than `[]`. A filter matching zero rows returned `{"tasks": null}`, so any client
iterating the array threw. The same bug was in four list handlers across three services.
**Fixed in commit `b7742d0`** with `make([]gin.H, 0, len(tasks))`; verified by hand —
`GET /tasks?status=Blocked` now returns `{"tasks": []}`.

**The suite is not idempotent, and this matters for every later stage.** Re-running it against
the same database yields **three** failures, none caused by the fix above:

1. `Register User` — 409, because it posts a hardcoded `microservices@test.com` that now
   exists. Does not cascade; `Login User` runs next and re-sets `auth_token`.
2. `Update Project` — 409, because it renames to a hardcoded `Updated Microservices Project`
   that already exists from the previous run.
3. `Filter Tasks by Status` — now a clean `expected 'Done' to deeply equal 'In Progress'`
   rather than a crash. The test is internally inconsistent: it queries `?status=Done` and
   then asserts the returned tasks are `In Progress`. The endpoint is correct; the test is
   wrong.

That third one turning from a `TypeError` into a value comparison is the proof the fix landed —
the response is now an array.

**So the standing rule while working through Stages 1–8:** either run the suite after a
`docker compose down -v` for a clean comparison, or run it as-is and expect exactly these
three. A *fourth* failure is a regression; these three are not. Stage 9 should make the
collection idempotent so this caveat stops being necessary.

Finally, record the state you are leaving behind:

```bash
cd task-service && go test ./...   # expect: "no test files"
```

That is here on purpose. Seeing `no test files` for every package once makes the first real
test in Stage 2 unmistakably the first.

- [x] Six containers up; any restart explained rather than shrugged at
- [x] All four direct `/health` endpoints report their service name and `healthy`
- [x] `/gateway/health` shows all four `*_status` fields `healthy`
- [x] Newman run saved; the single failure (`Filter Tasks by Status`) diagnosed as a real
      nil-slice bug and fixed in `b7742d0` — not the `Register User` failure predicted
- [x] Task creation returns 201 through the gateway
- [x] Confirmed no notification fires
- [x] `go test ./...` runs clean and reports no test files

---

### Stage 1 — Kafka broker in Compose

Add to `docker-compose.yml` only. No Go code, and therefore **no test** — see "what is
deliberately not unit tested" above. Verification is the shell below.

```yaml
  kafka:
    image: apache/kafka:3.9.0
    container_name: task-mgmt-kafka
    environment:
      KAFKA_NODE_ID: 1
      KAFKA_PROCESS_ROLES: broker,controller
      KAFKA_LISTENERS: INTERNAL://:29092,EXTERNAL://:9092,CONTROLLER://:9093
      KAFKA_ADVERTISED_LISTENERS: INTERNAL://kafka:29092,EXTERNAL://localhost:9092
      KAFKA_LISTENER_SECURITY_PROTOCOL_MAP: CONTROLLER:PLAINTEXT,INTERNAL:PLAINTEXT,EXTERNAL:PLAINTEXT
      KAFKA_INTER_BROKER_LISTENER_NAME: INTERNAL
      KAFKA_CONTROLLER_LISTENER_NAMES: CONTROLLER
      KAFKA_CONTROLLER_QUORUM_VOTERS: 1@kafka:9093
      KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR: 1
      KAFKA_GROUP_INITIAL_REBALANCE_DELAY_MS: 0
      KAFKA_AUTO_CREATE_TOPICS_ENABLE: "true"
      KAFKA_NUM_PARTITIONS: 3
    ports:
      - "9092:9092"
    healthcheck:
      test: ["CMD-SHELL", "/opt/kafka/bin/kafka-broker-api-versions.sh --bootstrap-server localhost:9092 >/dev/null 2>&1 || exit 1"]
      interval: 10s
      timeout: 10s
      retries: 10
      start_period: 15s
    restart: unless-stopped
    networks:
      - microservices
```

**Two listeners, not one — this is deliberate.** A single `PLAINTEXT://kafka:9092` listener
advertises the broker as `kafka:9092`, which only resolves inside the compose network. A client
on the host would connect to `localhost:9092`, be told "the broker is at `kafka:9092`", and fail
on a name it cannot resolve. That would break the Stage 4 integration tests, which run
`go test -tags=integration` from the host against a real broker. So there is an INTERNAL
listener advertised as `kafka:29092` for containers and an EXTERNAL one advertised as
`localhost:9092` for the host.

**Which address to use where:** services in Compose set `KAFKA_BROKERS=kafka:29092`. Host-side
integration tests and CLI tools use `localhost:9092`.

**`KAFKA_NUM_PARTITIONS: 3` matters more than it looks.** With auto-create enabled, a producer
that connects before the topic is explicitly created gets a topic with the broker default of
**one** partition. Everything would still work — and that is the danger, because with one
partition the ordering guarantee is trivially satisfied and the partition key provably does
nothing. The design would look correct while demonstrating nothing, and raising the partition
count later would break ordering in a way that was never tested. Setting the default to 3 means
even accidental auto-creation produces the right shape.

Follow the file's existing conventions: `container_name: task-mgmt-*`, the `microservices`
network, a healthcheck like `postgres` has.

Verify by hand before writing any producer:

```bash
docker compose up -d kafka
docker compose exec kafka /opt/kafka/bin/kafka-topics.sh --create \
  --topic task-events --bootstrap-server localhost:9092 --partitions 3
docker compose exec kafka /opt/kafka/bin/kafka-topics.sh --list \
  --bootstrap-server localhost:9092
```

- [x] Broker reaches healthy
- [x] `task-events` topic created with 3 partitions
- [x] Commit: `feat: add Kafka broker in KRaft mode`

**Leave `KAFKA_AUTO_CREATE_TOPICS_ENABLE: "true"` on** for local convenience, but know it is
a thing you would disable in production — a good small interview answer.

---

### Stage 2 — The event contract, test-first

The repo's first real test. No Kafka, no DB, no handlers — just the envelope and its JSON.
Starting here is the whole reason this plan was restructured: the wire contract is the thing
two services will agree on, so it gets pinned down before either of them exists.

**RED.** Write `task-service/internal/events/event_test.go` first, against types that are not
written yet. It will not compile, and that is the correct first failure. Assert:

- Marshaling an envelope produces exactly the JSON keys from section 3 —
  `event_id`, `event_type`, `occurred_at`, `task_id`, `actor_id`, `task`, `changes`. Unmarshal
  into a `map[string]any` and check the key set, rather than comparing a string; that survives
  field reordering but still fails on a rename.
- `occurred_at` round-trips as RFC 3339.
- `changes` is **absent** (not `null`, not `[]`) when there are none — i.e. the field carries
  `omitempty`. A `task.created` event should not ship an empty diff.
- A `ChangeDetail` marshals with the field names the monolith's `services.ChangeDetail` uses,
  so Stage 6 can port `email.go` without touching its signatures.
- `NewTaskCreated(task, actorID)` sets `event_type` to `task.created`, copies `task.ID` into
  `task_id`, and generates a non-empty, distinct `event_id` on two successive calls.

Run it: `cd task-service && go test ./internal/events/`. Confirm it fails to build.

**GREEN.** Write `internal/events/event.go`: the envelope structs, `ChangeDetail`, and the
`NewTaskCreated` constructor. Nothing else — no Kafka import in this package at all. Keeping
the contract package I/O-free is what keeps its tests instant.

Dependency for `event_id`:

```bash
cd task-service && go get github.com/google/uuid && go mod tidy
```

- [x] Test written first and observed failing to compile
- [x] `go test ./internal/events/` passes
- [x] `internal/events` imports no `kafka-go`, and never marshals `models.Task`
      directly — `TestTaskPayloadLeaksNoDatabaseFields` enforces the second half.
      (The original rule said "no gorm" too. That is unworkable: `internal/models`
      imports gorm, so any function taking a `models.Task` pulls it in transitively.
      What actually matters is keeping the Kafka client out and the DB model off the wire.)
- [x] Commit: `feat: add task event contract with tests`

---

### Stage 3 — The publisher behind an interface, test-first

**RED.** Write `internal/events/publisher_test.go` before the publisher. Define the seam the
test needs:

```go
type messageWriter interface {
    WriteMessages(ctx context.Context, msgs ...kafka.Message) error
}
```

`*kafka.Writer` satisfies this already, so production passes the real one and the test passes
a fake that appends to a slice and can be told to return an error. Assert:

- Publishing a created event calls the writer exactly once.
- The message **key** is the task ID as a string — this is the ordering guarantee from
  section 3, and it is the single most important assertion in this stage. Get it wrong and
  events for one task scatter across partitions, which no amount of manual clicking would
  reveal.
- The message value unmarshals back into an equal envelope.
- A writer error is **returned** from `Publish`, not logged and swallowed. The decision to
  ignore it belongs to the caller in Stage 4, not to this layer.

**GREEN.** Write `internal/events/publisher.go`: a `Publisher` struct over `messageWriter`,
`PublishTaskCreated(ctx, task, actorID) error`, and `Close()`. Read `KAFKA_BROKERS` and
`KAFKA_TOPIC` from env at construction, not at publish time.

```bash
cd task-service && go get github.com/segmentio/kafka-go && go mod tidy
```

- [x] Test written first and observed failing to compile on `undefined: Publisher`
- [x] Key-is-task-ID assertion present and passing
- [x] Writer errors propagate rather than being logged
- [x] Commit: `feat: kafka publisher with fake-writer tests`

Two things the stage turned up that the plan had not called for:

- **The balancer needed its own assertion.** kafka-go's zero-value `Balancer` is
  round-robin, which ignores the key entirely. `TestMessageKeyIsTaskID` passes
  perfectly well while every event scatters across the three partitions, so the
  key assertion alone does not protect the ordering guarantee —
  `TestWriterUsesHashBalancer` is what actually does. Verified by mutation:
  swapping in `&kafka.RoundRobin{}` leaves the key test green and fails only
  that one.
- **`Async` must stay false.** An async writer returns `nil` from
  `WriteMessages` immediately and reports failures to a callback, which would
  make "writer errors propagate" true against the fake and false in production.
  `TestWriterIsSynchronous` pins it.

Compose still passes no `KAFKA_BROKERS`/`KAFKA_TOPIC` to `task-service`; adding
them belongs with the wiring in Stage 4. Until then the constructor falls back to
`localhost:9092` / `task-events`, which is right for running the service on the
host and wrong inside the network — nothing calls `NewPublisher` yet, so nothing
depends on it.

---

### Stage 4 — Wire the publisher into the create handler, test-first

This is the stage that replaces the TODO at `handlers.go:112`.

> **Use plan mode for this stage.** It is the first one with real design choices rather than a
> single obvious shape: where the `Publisher` seam goes, whether the handler owns the interface
> or the events package does, how the publish error is swallowed without hiding it, and how far
> the sqlmock harness should reach. Stages 0–2 did not need it — Stage 0 was pure verification
> and plan mode actively got in the way there, since the plan-doc corrections it turned up
> could not be written until after exiting. Here the cost is inverted: getting the seam wrong
> means redoing Stage 4 and part of Stage 7.

**RED.** Write the handler test first. This one needs the DB, so it is the Layer-2 sqlmock
setup from section 4. Define a `Publisher` interface in the handlers package (consumer-side
interface, so handlers depend on what they use) and inject a fake. Assert:

- A successful create publishes exactly one `task.created` carrying the ID the DB assigned —
  which proves the publish happens **after** the insert, since the ID does not exist before it.
- **A publish failure still returns 201.** Make the fake return an error and assert the status
  code. This is the decoupling guarantee as an executable claim: the task is already
  committed, so a 500 would tell the client something false.
- A failed DB insert returns 500 and publishes **nothing**.

That middle assertion is the one to write with care. It is the design of the whole system
reduced to one test, and it is exactly the property a future refactor would break by
"improving" error handling.

**GREEN.** Add the publisher field to the handler, construct it once in `task-service/main.go`
with `defer publisher.Close()`, and publish after the `database.DB.Create(&task)` error check.
Log publish failures; return 201 regardless.

**Then the integration test**, in a new `//go:build integration` file: with the Compose stack
up, create a task through the handler and read the event back off the real broker with a
`kafka.Reader`. This is the first end-to-end proof, and it is tagged so it never slows the
unit loop.

Manual confirmation with the console consumer is still worth doing once:

```bash
docker compose exec kafka /opt/kafka/bin/kafka-console-consumer.sh \
  --topic task-events --bootstrap-server localhost:9092 --from-beginning
```

- [x] Handler tests written first and observed failing on `undefined: Publisher`
- [x] "Publish failure still returns 201" test passes
- [x] `go test -tags=integration ./...` reads the event off a real broker
- [x] `go test ./...` still passes with Docker stopped
- [x] TODO at `handlers.go:112` deleted
- [x] Commit: `feat: publish task.created from task-service`

**Decisions taken.** Publishing is synchronous and capped at 2s
(`handlers.publishTimeout`), on a context derived from `Background` rather than
the request, so a client that hangs up cannot cancel an event for a task that was
genuinely committed. The publisher hangs off an exported package-level
`handlers.Publisher`, mirroring `database.DB`, because this service has no
dependency injection anywhere and introducing it for one field would have buried a
ten-line change in a two-hundred-line diff. The `eventPublisher` interface is
declared in `handlers`, by the consumer, which keeps `kafka-go` out of the handler
tests entirely.

**Two defects that only the real stack could surface.** Both were invisible to the
fake-writer tests by construction, and both are now covered by config assertions
and `//go:build integration` tests:

1. **The first publish against a fresh broker always failed**, losing exactly one
   event per environment, silently. With auto-creation on, `Writer.partitions`
   (kafka-go `writer.go:744`) sends a metadata request that *triggers* the
   creation and is then answered with `UnknownTopicOrPartition`, because the topic
   does not exist yet when the broker builds that response. That error is raised
   upstream of kafka-go's own produce-path retries, so it reached the caller
   unretried. Fixed with a bounded retry in `publish` that only retries errors
   kafka-go itself classifies as temporary; `TestFirstPublishToNewTopicSucceeds`
   covers it against a genuinely new topic.
2. **Every create paid a full second of latency.** kafka-go holds a synchronous
   write open for `BatchTimeout` waiting to batch, and defaults that to 1s — which
   a service publishing one message per request pays in full, every time. Setting
   it to 10ms took creates from ~1000ms to ~16ms. `TestWriterBatchTimeoutIsShort`
   pins it.

Both belong to the same family as the Stage 3 balancer bug: a test against a fake
can only prove the code is right *given* the real client behaves like the fake, so
anything the client decides for you by default needs its own assertion.

**Measured end to end**, against a stack rebuilt from `docker compose down -v`:

| Scenario | Result |
|---|---|
| First create, broker has never seen the topic | 201 in 154ms, event published |
| Subsequent creates | 201 in ~16ms |
| Create with `kafka` stopped | 201 in 27–320ms, body unchanged, error logged, task persisted |
| Create after `kafka` restarted | 201 in 42ms, publishing resumes with no intervention |

Events for different tasks hash across partitions; `TestSameTaskAlwaysSamePartition`
proves five events for one task all land on one, against a real broker rather than
against an assertion about the balancer field.

**The known gap, unchanged and deliberate.** Events published while the broker was
down are gone — two of six tasks in the run above produced no event. Publishing
happens after the commit with no record of the intent, so nothing can replay them.
That is what the transactional outbox in Stage 8 would fix, and naming it honestly
is part of the deliverable.

Compose now passes `KAFKA_BROKERS=kafka:29092` and `KAFKA_TOPIC=task-events` to
`task-service`, with `depends_on: kafka: condition: service_started` — deliberately
not `service_healthy`, since waiting on a healthy broker would rebuild at boot
exactly the coupling this design removes, and would push the gateway's startup out
behind Kafka's too.

---

### Stage 5 — notification-service consumer, test-first (log only, no email)

New top-level directory `notification-service/`, structured like the existing services:
`main.go`, `Dockerfile` (copy `task-service/Dockerfile`, no port to expose), `go.mod` with
module name `task-management-notification-service`. Copy `internal/events/event.go` from
`task-service` — separate modules, so this is a duplicated contract, which is the standard
trade in a polyrepo-style layout. Note it in the file header.

This stage only reads events and logs them. Consumer-group mechanics are easier to get right
without SMTP in the way.

**RED.** The testable part is not the reader loop — it is the per-message decision. Write
`internal/consumer/handler_test.go` first, against a pure
`handleEvent(ctx, envelope) error`, table-driven:

- A `task.created` envelope is handled and reports which action it took.
- An **unknown `event_type` is skipped, not an error.** Forward compatibility: a future
  producer adding `task.archived` must not crash or stall this consumer.
- **Malformed JSON returns an error without panicking.** The loop must survive a bad message;
  write that as a test rather than trusting it.
- An envelope with `actor_id` of 0 is rejected, since there is no one to notify.

**GREEN.** Write `handleEvent`, then the loop around it: `kafka.NewReader` with
`GroupID: "notification-service"` — a group ID, not a bare partition reader, so offsets are
tracked and the service resumes where it left off. Log with the `[NOTIFICATION-SERVICE]`
prefix, matching `task-service/main.go`'s `gin.LoggerWithFormatter` style. Handle `SIGTERM`
and close the reader cleanly so offsets commit on shutdown.

Add to Compose with `depends_on: kafka: condition: service_healthy` and the same
`DATABASE_URL`/`microservices` network as the others.

**Manual verification — the part no test covers.** Create three tasks, watch three log lines.
Then `docker compose restart notification-service` and create another: it should pick up only
the new event, not replay all four.

- [x] `HandleMessage` tests written first and observed failing on `undefined: Action`
- [x] Unknown-event-type and malformed-JSON cases pass
- [x] Events logged as they arrive
- [x] Restart does not replay already-consumed events
- [x] `docker compose stop notification-service`, create 2 tasks, start it again — both
      arrive. **This is the payoff of the whole design; see it work.**
- [x] Commit: `feat: notification-service consumer group with handler tests`

**Shape.** The per-message decision (`HandleMessage`) is a pure function of the
message bytes and holds every test; the loop (`Run`) owns connections, offsets
and signals and makes no decisions. `HandleMessage` returns a `Result{Action,
Summary}` rather than logging, so the loop can print a useful line without
decoding the message a second time.

**The contract is duplicated, not imported.** `notification-service/internal/events`
is a hand-trimmed copy of the task-service package: separate Go modules, so there
is no import path, and a `replace` directive would couple the build graphs this
work exists to separate. The copy is deliberately *not* identical — it carries
only the types a consumer needs, omitting the producer-side constructors and the
`models.Task` import they require, which is what keeps gorm and any database
dependency out of this service entirely. `TestDecodesProducerWireFormat` decodes
a message captured verbatim from the Stage 4 console consumer, so a rename on the
producer side fails here rather than silently arriving as a zero value.

**Deliberate asymmetry in Compose.** `task-service` depends on Kafka with
`service_started`; `notification-service` uses `service_healthy`. That is the
design stated as configuration: a producer must never wait on the broker, because
a write path cannot take on a side effect's availability, whereas a consumer has
nothing to do until the broker is up and waiting costs it nothing. The consumer
has no port and no healthcheck either — it serves no HTTP, so `docker compose ps`
shows plain `Up`, and liveness is read from its logs or its committed offsets.

**One trap, found and fixed.** The first version of the reader config tests called
`NewReader`, which is side-effecting: kafka-go immediately spawns a goroutine to
join the group and `Close` blocks until it finishes. The tests passed in 5.9s
*because a broker happened to be running* — they had quietly become integration
tests. Settings now live in a pure `readerConfig`, the suite runs in 0.4s, and it
was re-verified with the entire stack stopped.

**Observed end to end**, all four checks against the live stack:

| Check | Result |
|---|---|
| Brand-new group starts | Picked up the 4-event backlog already on the topic (`StartOffset: FirstOffset`) |
| Create 3 tasks | 3 log lines, one per event, across 3 partitions |
| `restart notification-service` | **0** events replayed — committed offsets held |
| `stop`, create 2 tasks, `start` | Both delivered on startup, nothing else replayed |
| Garbage produced onto the topic | Both poison messages `DISCARDED`, offset committed, and a real event behind them processed — the partition did not wedge |

Consumer group lag reads 0 on all three partitions.

---

### Stage 6 — Real emails, test-first

Port `monolith/internal/services/email.go` into
`notification-service/internal/services/email.go`.

- Copy it, do not import it. The monolith is a separate Go module
  (`github.com/P4rz1val22/task-management-api`) and is the old side of the Strangler Fig —
  reaching into it would recouple what this work is decoupling.
- `SendTaskCreatedNotification(task, userEmail)` and
  `SendTaskUpdatedNotification(task, userEmail, changes)` keep their exact signatures, which
  is why the event envelope was shaped to match.
- **One additive change the ported copy does need:** the monolith's `services.ChangeDetail`
  declares `Field`, `From`, `To` with *no JSON tags at all*, so Go would marshal them
  capitalised. `events.ChangeDetail` declares the lowercase wire names. The Go field names
  match 1:1 — so no signature changes — but the ported struct needs
  `json:"field"`/`json:"from"`/`json:"to"` added to unmarshal from the envelope.

**RED, and this is the interesting bit:** `email.go` has never had a test in its life. Port it
first, then write the tests against the ported copy before changing a line of it — the tests
are what let you refactor it afterward. To make it testable, extract the actual SMTP call
behind a one-method interface:

```go
type mailSender interface {
    Send(to []string, msg []byte) error
}
```

Then assert, with a fake sender:

- The rendered body of an update email contains every changed field and **no unchanged
  field**. Table-driven over a few change sets. This is the behavior a user would notice
  breaking, and it is pure string rendering — ideal for a test.
- With `SMTP_USERNAME`/`SMTP_PASSWORD` unset, nothing is sent and the "SMTP not configured -
  would send" path is taken. The monolith's graceful degradation becomes a guarantee instead
  of an accident.
- Recipient lookup: given an `actor_id`, the email goes to that user's address. This is the
  `database.DB.First(&user, userID)` from the monolith's goroutine, now keyed off the event —
  sqlmock again.

**GREEN.** Wire `handleEvent` to call the email service. Put `SMTP_USERNAME`/`SMTP_PASSWORD`
in Compose for this service.

- [x] Ported `email.go` has tests before any modification
- [x] Change-rendering test covers "unchanged fields absent"
- [x] Task creation produces a "would send" log with SMTP unset
- [ ] With real credentials, an email actually arrives — **deliberately not done.**
      SMTP is left unconfigured; `SMTP_USERNAME`/`SMTP_PASSWORD` are present but
      empty in Compose, so the service renders every email and logs
      `would send: <subject> to <address>`. Turning it on is two environment
      variables and no code change.
- [x] Commit: `feat: send task notifications from notification-service`

**Four deliberate deviations from a straight port**, all documented in the file
header:

1. **`Send*Notification` return an error.** The monolith's versions logged and
   returned nothing, which was survivable inside a fire-and-forget goroutine and
   is not here — the consumer has to tell a transient failure from a permanent
   one, and it cannot do that with a swallowed error. This is the one place the
   plan said to keep signatures identical and the port does not.
2. **They take `events.TaskPayload`, not `models.Task`.** The payload is what
   arrives on the wire, and taking it directly keeps gorm out of this package.
3. **There is no `services.ChangeDetail`.** The plan expected to port that
   struct and add JSON tags; since the diff already arrives as
   `events.ChangeDetail` with the right tags, a second identical struct would be
   pure duplication. The Go field names still match the monolith's, which is
   what the plan actually needed.
4. **SMTP sits behind a one-method `mailSender`,** so rendering is testable
   without a mail server. Also fixed while porting: the original logged
   `SMTP_USERNAME` in the clear at startup. Both credentials are masked now.

**Recipient lookup** is `internal/users`, reading the shared `users` table keyed
off the event's `actor_id`. The envelope carries a user ID rather than an address
on purpose — an address is mutable personal data and freezing it into every event
on the topic would be wrong. The query selects the `email` column alone rather
than the row, so the bcrypt hash never enters this process;
`TestLookupSelectsOnlyTheEmailColumn` enforces that against the SQL GORM actually
emits. `internal/database` deliberately does **not** call `AutoMigrate`, so this
service does not join the four-way migration race documented in CLAUDE.md.

**Stage 6 activated a latent hole, and closing it was in scope.** Until this
stage every failure the handler could produce was permanent, so the loop
committing after a failure was harmless. A live email path makes a database
outage or an unreachable mail server able to fail a message that would succeed
moments later — and the loop would have committed it away with only a log line.
The fix is a bounded retry (3 attempts, 500ms apart) that skips permanent errors
entirely. It is **not** the full answer: after the retries are exhausted the
offset is still committed and the event is genuinely lost, logged as
`EVENT LOST after 3 attempts`. Stage 8's dead-letter topic replaces that branch.
Not committing instead would wedge the partition and lose everything behind it,
which is strictly worse.

**The error classification is the load-bearing decision here**, and both
directions are tested because both fail silently:

| Condition | Classification | Consequence |
|---|---|---|
| Malformed JSON, missing `event_id`/`actor_id` | permanent | discarded, offset committed |
| User does not exist, or has no address | permanent | discarded — no retry invents an address |
| Postgres unreachable | transient | retried, then lost with a loud log |
| SMTP unreachable | transient | retried, then lost with a loud log |

**Observed end to end:**

| Check | Result |
|---|---|
| Create a task through the gateway | Address resolved from Postgres via `actor_id`, email rendered, `would send` logged |
| `docker compose stop postgres`, publish an event | 3 attempts, then `EVENT LOST` naming the real cause; partition kept moving |
| `docker compose start postgres`, create a task | Notifications resume with no intervention |

**Version pin worth knowing:** `go mod tidy` initially resolved gorm 1.31.2,
which requires Go 1.25 and broke the 1.24.5 Alpine build. Pinned to gorm 1.30.1 /
driver-postgres 1.6.0 / pgx 5.6.0 to match `task-service`. `go.sum` mentions
`go-sqlite3` from walking the module graph; it is not imported, not in the build
graph, and `CGO_ENABLED=0` builds clean.

---

### Stage 7 — `task.updated` and `task.deleted`, test-first

The best TDD fit in the plan, because the diffing logic is pure and the rules are fiddly.

**RED.** Write the diff test before the diff. Extract it as
`events.DiffTask(before, after models.Task) []ChangeDetail` — a free function over two
structs, no gorm, no gin. Table-driven:

- One field changed → exactly one `ChangeDetail`, with the right `from` and `to`.
- Several fields changed → one entry each, and assert the **order is stable**, or the test
  will flake once someone iterates a map.
- **Nothing changed → an empty slice.** This is the "a save that changed nothing sends no
  email" rule, and having it as a test is the whole argument for this stage's ordering.
- A `nil` → set `DueDate` (it is a `*time.Time`) renders sensibly rather than printing a
  pointer address. Easy to get wrong, invisible until it reaches an inbox.

Then a handler test: an update that changes nothing publishes **zero** events.

**GREEN.** Uncomment the `originalTitle`/`originalStatus`/`originalPriority`/`originalEstimate`
capture at `handlers.go:322`, call `DiffTask`, publish `task.updated` where the TODO at
`handlers.go:342` sits, and `task.deleted` in `DeleteTask` after `database.DB.Delete(&task)`.
Skip publishing when `changes` is empty. Branch on `event_type` in the consumer;
`task.deleted` has no existing email template — either add a small one or skip sending, and
write down which in this file.

- [x] `DiffTask` tests written first and observed failing
- [x] No-op-update-publishes-nothing test passes
- [x] All three TODO comments now deleted from `handlers.go` — `grep -rn TODO
      task-service/internal/` returns nothing, and the commented-out
      `originalTitle`/`originalStatus`/… block is gone with them
- [x] An update email lists exactly the fields that changed
- [x] Commit: `feat: publish task.updated and task.deleted events`

**Diff scope widened past the monolith, deliberately.** The monolith compared
four fields — Title, Status, Priority, Estimate — so editing a description, a due
date, or moving a task between projects notified nobody. That reads more like an
oversight than a decision, so `DiffTask` covers all seven user-visible fields.
Bookkeeping columns stay out: `UpdatedAt` moves on every save, and counting it
would make every no-op update look real and defeat the entire rule.

**Deletion gets its own email.** The plan left this open. Building it turned out
to be near-free — the HTML wrapper is already shared by the other two, so it is a
content block and a subject line — and *not* building it would have left a bug:
the consumer routed everything that was not an update to the **created**
template, so a `task.deleted` would have emailed the user "New Task Created"
about a task they had just deleted. Unreachable until this stage published
deletes, at which point it becomes real. The switch is now explicitly exhaustive
with a `default` that sends nothing, so a future event type cannot mail the wrong
template.

**Three rules worth their tests, all confirmed by mutation:**

1. *Nothing changed means no event.* Forcing the handler to publish
   unconditionally fails `TestUpdateWithNoChangesPublishesNothing` alone.
2. *The diff is taken against the pre-update snapshot.* Diffing the task against
   itself fails the two tests that check `from` values.
3. *Due dates compare by value, not by pointer.* `DueDate` is a `*time.Time`, and
   comparing pointers makes **every** diff report a spurious change — the
   mutation failed even the "nothing changed" case, because each construction of
   the fixture allocates a fresh pointer. A JSON round trip does exactly the same
   thing. An unset date renders as the word `none`, never an address.

**One thing the mocks surfaced about existing code.** `UpdateTask` loads the task
with `Preload("Project")`, and GORM auto-saves loaded associations, so every task
update also issues `INSERT INTO "projects" … ON CONFLICT DO NOTHING`. Harmless —
the conflict clause makes it a no-op — but it is real, and the test harness
models it rather than pretending otherwise.

**Observed end to end**, one task through its whole life:

| Request | Result |
|---|---|
| Create | `task.created`, partition 2, key 23 |
| Update changing **nothing** | HTTP 200, **no event published at all** |
| Update: status, priority, due date | `task.updated` listing exactly `Status(Not Started->Done) Priority(Low->Urgent) Due Date(none->2026-12-25)` |
| Delete | `task.deleted`, deletion email — not the creation one |

All three events landed on **partition 2 at consecutive offsets 8, 9, 10** under
key `23`. That is the ordering guarantee the single-topic design was built for,
visible for the first time: until this stage only one event type existed, so
nothing could have violated it.

---

### Stage 8 — Resilience, test-first (optional, and the interview material)

Each of these is a behavior you can only really demonstrate with a test, because the failure
it prevents is hard to trigger by hand. In rough order of value per hour:

1. **Idempotent consumption.** RED: handling the same `event_id` twice sends exactly one
   email. That single assertion is the entire feature. GREEN: a `processed_events` table
   keyed on `event_id`, checked before sending. Closes the duplicate-email hole that
   at-least-once delivery creates — and note that you cannot show this by clicking around,
   which is precisely why it gets a test.
2. **Retry with a dead-letter topic.** RED: a sender that fails N times is retried N times
   and then produces one message on `task-events.dlq`, with the offset committed. Assert the
   offset commit — that is what stops one bad event from blocking the partition forever.
   Head-of-line blocking is a real Kafka failure mode and worth being able to talk about.
3. **Transactional outbox.** The honest remaining gap: Stage 4 commits to Postgres and *then*
   publishes, so a crash in between loses the event. The fix is writing the event to an
   `outbox` table inside the same transaction as the task, with a separate poller publishing
   from it. RED here is a test that the event row and the task row commit or roll back
   together. This is the strongest thing on the plan to be able to explain, and also the most
   work.

   **Split into its own stage, and not skipped.** The original note here suggested stopping
   after 1 and 2 and simply knowing the gap exists. That was reconsidered: this project's
   stated justification is decoupling and reasoning about failure, so a write path that can
   silently drop events is a hole in precisely the dimension the work claims to be about.
   Its size is a scheduling question, not a reason to drop it — see Stage 8b.

- [x] Idempotent consumption
- [x] Retry with a dead-letter topic
- [x] Transactional outbox — built as **Stage 8b**, below

**Sequencing was a dependency, not just cost order.** Item 1 had to come first because item 3
needs it: an outbox poller publishes a row and then marks it sent, so a crash between those
two republishes the event. The outbox *creates* duplicates by design, and is only safe on top
of an idempotent consumer.

**Deduplication.** `processed_events`, keyed on `event_id` — the field the envelope has
carried since Stage 2, put there for exactly this. Recorded **after** a successful send, never
before: recording first would mean a failed send is never retried, because the redelivery
would be waved through as a duplicate and the notification lost silently. Checked before the
recipient lookup, so a redelivery costs one query rather than two and an SMTP round trip. A
store outage is never read as "not seen" — that would send duplicates during exactly the
moment things are already going wrong — and is classified transient, not poison.

The residual window is irreducible and worth naming: a crash between sending the email and
recording the row replays that one email. Closing it would need the send and the database
write to be a single atomic act, and SMTP does not participate in database transactions.

**Dead-letter topic.** `task-events.dlq`, derived from the source topic name rather than
separately configured, so a typo can never point them at the same topic and loop failures back
forever. The original key and value travel byte for byte so a message stays replayable;
diagnostics ride in headers. This is `notification-service`'s first *producer* — until now it
only read.

The load-bearing assertion is the one the plan singles out: **if the dead-letter write fails,
the offset is not committed.** Committing a message that was preserved nowhere is the silent
loss the whole feature exists to prevent.

**A real bug, found by running it, in the most instructive way available.** The first
dead-letter write failed with `UnknownTopicOrPartition` — the identical topic auto-creation
race fixed in `task-service` in Stage 4, which was never carried across to this new producer.
What happened next is the point:

- The safety property **held**. The write failed, so the offset was correctly not committed,
  and nothing was lost.
- Liveness did **not**. The poison messages could not reach the one place built to hold them,
  and partition 0 stalled at lag 2 until the service was restarted.

Fixed with the same bounded retry over kafka-go's temporary errors. On restart both messages
drained into `task-events.dlq` with full headers and lag returned to zero on all three
partitions. The general lesson is that a correct safety property can still leave a system
stuck, and that a fix applied to one producer does not travel to the next one for free.

**Observed end to end:**

| Check | Result |
|---|---|
| `processed_events` table | Created with `event_id` as primary key |
| Create a task | One email, one row recorded |
| Replay the **identical** event onto the topic | Logged `duplicate`, **no second email**, still one row |
| Two poison messages, then a real event | Both `DEAD-LETTERED to task-events.dlq`, real event processed, lag 0 |
| Dead-letter contents | Original key and bytes intact, headers naming the error, source topic, partition, offset and time |

---

### Stage 9 — Docs and close the loop

By this point every Go test is already written, so what is left is the API-level suite and
the docs — which is the point of the restructure.

- **Get the Postman collection into git.** Take a **fresh** export of
  `Task Management Microservices API` from the app and commit it to `docs/postman/`, alongside
  the monolith collection. Do not reuse the August 2025 export sitting in `~/Downloads` — the
  live collection is the cloud copy (see section 4) and that file is a stale snapshot. Diff
  the fresh export against the old one first; whatever shows up is drift you accumulated
  without noticing, which is the argument for keeping it in git at all. Commit the export
  before adding to it, so the diff of the addition is reviewable rather than buried in a
  23KB blob.
- **Optional, but it is what makes the habit stick:** fetch the collection via the Postman
  API (`GET https://api.getpostman.com/collections/{id}` with an `X-Api-Key` header) from a
  small script, so re-exporting is a command rather than a five-click manual chore. Needs an
  API key from Postman account settings. A habit that depends on remembering File → Export
  is a habit that stops after twice.
- **Then add to it**, matching the existing style — a real `pm.test()` with body assertions,
  not a status-code check, and reuse the `auth_token` / `task_id` collection variables rather
  than introducing new ones. The test to add: task creation still returns 201 with Kafka
  stopped. That encodes the decoupling guarantee at the HTTP layer, complementing the Go unit
  test from Stage 4 — the Go test proves the handler ignores the publish error, and this
  proves the whole stack does.
- Optionally, close one of the gaps section 4 names while you are in there: add tests to the
  three direct service-health requests that have none. Cheap, and it stops "17 requests, 15
  tests" from being a number you have to explain.
- **Make the collection idempotent**, which Stage 0 proved it isn't. `Register User` and
  `Update Project` both post hardcoded values that collide on a second run, and
  `Filter Tasks by Status` asserts `In Progress` while querying `?status=Done`. Randomize the
  email and project name (a pre-request script, or `{{$randomEmail}}`) and fix that assertion
  to match its own query. Until this is done, a re-run costs three false failures, which is
  exactly how a suite stops being trusted.
- **README** — update the architecture section and diagram to show the broker and the
  consumer. Add a "Running the tests" section documenting the two-tier split (`make test` for
  unit, `make test-integration` for the tagged ones) — an undocumented build tag is an
  invisible test suite.
- Record the final counts (services, event types, test count from `go test ./... -v`) — they
  go in the Knowledge Bank.

---

## 6. Things to deliberately not do

- Do not put Kafka publishing in the monolith. It is the old side of the migration; leave it.
- Do not have `notification-service` call `task-service` over HTTP for task data. The event
  is self-contained on purpose.
- Do not add Schema Registry / Avro / Protobuf. JSON is right at this scale and adds a
  container plus a codegen step for no benefit here.
- Do not add Kubernetes in the same pass. Compose is the right tool for a local 7-container
  stack, and mixing two infra changes makes both harder to verify.
- Do not switch to `confluent-kafka-go`. See section 2.
- Do not write a test that mocks a Kafka broker wholesale and asserts the mock was called.
  Fake the narrow interface you own (`messageWriter`, `mailSender`); use a real broker behind
  the `integration` tag for anything else.
- Do not let integration tests run under a bare `go test ./...`. The build tag is what keeps
  the fast loop fast, and a suite that needs Docker to pass is a suite people stop running.
- Do not add a testing framework. `testing` plus table-driven subtests is enough, and it
  matches a stdlib-leaning repo. If assertions get painful later, `testify` is a small step,
  but do not start there.

---

## 7. After the code: close the loop

1. **Log it to the Knowledge Bank** via the `resume-knowledge-bank` skill — confirmed tier,
   solo work, with the real numbers from Stage 9. Include the honest framing this repo
   already carries: self-directed portfolio work, not production traffic. On testing, the
   accurate claim is *extended* coverage, not *introduced* it — the project already had a
   17-request Postman suite at the API boundary, and this work adds Go unit coverage below
   that boundary where the event logic lives. That distinction is worth getting right,
   because "added tests to an untested project" is the kind of line an interviewer can
   puncture in one question.
2. **Re-tailor the Chewy resume** (`~/resumes/data/chewy_autoship_swe.js`). The Task
   Management Microservices project entry gets rewritten around the event system, which
   converts Kafka, event-driven architecture, and "loosely coupled components" from gaps
   into evidence.
3. The line worth landing, once it is true: *replaced in-process goroutine notifications
   with a durable Kafka consumer group, so task writes no longer depend on mail delivery.*
   That is a design decision with a reason, which reads better than a tool list.


---

### Stage 8b — Transactional outbox

The last real hole, closed. `task-service` used to commit the task and then publish,
which is two things that were not one thing: a crash or an unreachable broker in
between left the task existing and the event never happening, with nothing anywhere
that could replay it. Retrying could never fix that, because the failure can be the
process disappearing, and no code runs after a crash.

Now the event is written into an `outbox` table **inside the same transaction as the
task**. The two rows commit together or neither exists. A poller publishes from that
table on a one-second tick and marks rows sent.

- [x] Outbox tests written first and observed failing
- [x] Create/update/delete each write task + outbox in one transaction
- [x] Outbox-insert failure rolls back the task (the headline assertion)
- [x] No-op update still writes nothing
- [x] Poller publishes in `id` order and stops the batch on first failure
- [x] `publishTimeout`, `handlers.Publisher` and `noopPublisher` deleted
- [x] Tasks created with Kafka stopped are published automatically once it returns
- [x] Commit: `feat: transactional outbox for task events`

**The guarantee moved rather than disappeared.** Stage 4's headline test was "a publish
failure still returns 201". There is no publish left to fail: the outbox write is a
local insert in the same transaction, so it fails only when the database fails, in
which case the task creation was going to fail anyway. That test became
`TestCreateTaskRollsBackWhenTheEventCannotBeRecorded` — the same guarantee, asserted
where it now lives. sqlmock matches expectations in order, so the create tests are a
real claim about the shape of the transaction: BEGIN, task, event, COMMIT.

**The request path no longer knows Kafka exists.** `publishTimeout`,
`handlers.Publisher`, the `eventPublisher` interface and `noopPublisher` are all
deleted. A create used to wait up to 2s when the broker was down; now it never
contacts the broker at all, so that ceiling is gone along with the code that needed it.

**Two properties in the poller carry the design:**

1. **Rows publish in `id` order**, which is insertion order, which is the order the
   events happened. With the partition key, that is what keeps a `task.updated` from
   overtaking its own `task.created`.
2. **A failure stops the batch.** Skipping a failed row and continuing would publish
   a later event for a task before an earlier one — the exact reordering the
   single-topic design exists to prevent, happening precisely when the broker is
   flaky and nobody is watching closely.

There is deliberately **no transaction around a batch**. Holding one open across a
network round trip per row would keep locks for the length of a Kafka outage. A second
poller could therefore publish a row twice, which the consumer already deduplicates —
the same duplicate this design accepts by construction. Ordering, however, assumes a
single poller, which is what runs.

**What it still does not buy, stated plainly.** Not exactly-once: the poller publishes
a row and then marks it sent, so a crash between those republishes the event. That is
by design, and it is why Stage 8's deduplication had to land first — an outbox
*creates* duplicates and is only safe on top of an idempotent consumer. It also adds
up to one poll interval of notification latency, which for email is invisible.

**Observed end to end — the scenario that had failed since Stage 4:**

| Step | Result |
|---|---|
| `docker compose stop kafka`, create two tasks | Both 201, and fast — the request never touches the broker |
| Inspect the database | Two `outbox` rows, `sent_at` NULL: the events exist **as data**, not as a lost log line |
| Poller meanwhile | Retries every second, logging the real dial error rather than pretending |
| `docker compose start kafka` | Within a tick: `outbox published 2 events`, both rows `sent_at` set, `attempts = 1` |
| notification-service | Emailed both — the two notifications that were previously lost forever |

Consumer lag returned to zero on all three partitions with no intervention.