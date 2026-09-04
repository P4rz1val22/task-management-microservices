//go:build integration

// End-to-end coverage for the transactional outbox, against the real stack.
//
// This is the one guarantee in the system that cannot be demonstrated with a
// mock, because the thing being tested is what happens when a dependency is
// genuinely absent. The unit tests in internal/outbox prove the task and its
// event share a transaction; they cannot prove that a task created during a
// broker outage is still delivered afterwards, because that claim spans a
// process restart, a database and a broker.
//
// The scenario below is precisely the one that used to lose data. Before the
// outbox, task-service committed the task and then published, so an unreachable
// broker left the task existing and the event never happening, with nothing
// anywhere that could replay it. Now the event is a row, so it survives.
//
// This test STOPS AND STARTS the kafka container. It restores it on cleanup, but
// do not run it against a stack anyone else is using.
//
//	docker compose up -d
//	cd task-service && go test -tags=integration -timeout 5m ./internal/e2e/
//
// `go test -p 1` is required when running this alongside other packages, and is
// why `make test-integration` passes it. Go runs separate packages in parallel by
// default, so without it this package stops the broker out from under the
// publisher tests in internal/events and they fail for a reason that has nothing
// to do with them.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"task-management-task-service/internal/outbox"
)

const (
	// The gateway's host port. Containers reach each other by service name on
	// the compose network; a host process must use the published port.
	defaultGatewayURL = "http://localhost:8081"

	// Postgres on the host. Note this is localhost:5432, not the compose
	// hostname `postgres` that the services themselves use.
	defaultTestDSN = "postgres://postgres:password123@localhost:5432/taskmanagement?sslmode=disable"

	// How long to wait for the poller to drain a row once the broker is back.
	// The poller ticks once a second, but the broker itself needs time to
	// finish starting and answer a metadata request, so this is deliberately
	// far longer than the tick.
	drainTimeout = 90 * time.Second
)

func gatewayURL() string {
	if u := os.Getenv("GATEWAY_URL"); u != "" {
		return u
	}
	return defaultGatewayURL
}

// testDSN reads TEST_DATABASE_URL rather than DATABASE_URL on purpose.
//
// The repository's root .env sets DATABASE_URL to a hosted Neon database. If
// this test picked that up it would silently assert against the wrong database
// and pass or fail for reasons that have nothing to do with the code under test.
func testDSN() string {
	if d := os.Getenv("TEST_DATABASE_URL"); d != "" {
		return d
	}
	return defaultTestDSN
}

// repoRoot is where docker-compose.yml lives, three levels up from this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	if r := os.Getenv("REPO_ROOT"); r != "" {
		return r
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "docker-compose.yml")); err != nil {
		t.Fatalf("no docker-compose.yml at %s (set REPO_ROOT): %v", root, err)
	}
	return root
}

// compose runs a docker compose subcommand from the repository root.
func compose(t *testing.T, args ...string) {
	t.Helper()
	root := repoRoot(t)

	cmd := exec.Command("docker", append([]string{"compose"}, args...)...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker compose %v: %v\n%s", args, err, out)
	}
}

// requireStack fails fast with a usable message rather than hanging, if the
// stack is not up. It returns a gorm handle on the real database.
func requireStack(t *testing.T) (string, *gorm.DB) {
	t.Helper()
	base := gatewayURL()

	// /gateway/health, not /health: the gateway does not serve /health, and a
	// request to it is answered by the monolith instead.
	resp, err := http.Get(base + "/gateway/health")
	if err != nil {
		t.Fatalf("no gateway at %s (is `docker compose up -d` running?): %v", base, err)
	}
	resp.Body.Close()

	// Silence GORM's logger: this test deliberately provokes errors elsewhere in
	// the stack, and query logs here only make the real failure harder to find.
	db, err := gorm.Open(postgres.Open(testDSN()), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("no database at %s (set TEST_DATABASE_URL): %v", testDSN(), err)
	}
	return base, db
}

func uniqueSuffix() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), rand.Intn(1000))
}

func do(t *testing.T, method, url, token string, body any) (int, map[string]any) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s body: %v", method, url, err)
	}

	var decoded map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("%s %s returned %d with a non-JSON body %q",
				method, url, resp.StatusCode, raw)
		}
	}
	return resp.StatusCode, decoded
}

// newProject registers a user and gives it a project, returning the token and
// the project's ID. Both are minted fresh so the test is re-runnable.
func newProject(t *testing.T, base string) (string, float64) {
	t.Helper()

	status, body := do(t, http.MethodPost, base+"/auth/register", "", map[string]any{
		"name":     "Outbox E2E",
		"email":    "outbox-e2e-" + uniqueSuffix() + "@test.com",
		"password": "password123",
	})
	if status != http.StatusCreated {
		t.Fatalf("register returned %d, want 201: %v", status, body)
	}
	token, ok := body["token"].(string)
	if !ok || token == "" {
		t.Fatalf("register returned no token: %v", body)
	}

	status, body = do(t, http.MethodPost, base+"/projects", token, map[string]any{
		"name":        "Outbox E2E Project " + uniqueSuffix(),
		"description": "Created by the outbox integration test",
	})
	if status != http.StatusCreated {
		t.Fatalf("create project returned %d, want 201: %v", status, body)
	}
	project, ok := body["project"].(map[string]any)
	if !ok {
		t.Fatalf("create project returned no project object: %v", body)
	}
	projectID, ok := project["id"].(float64)
	if !ok {
		t.Fatalf("created project has no numeric id: %v", project)
	}
	return token, projectID
}

// The scenario that used to lose the event outright.
//
// Three claims, in the order they matter:
//
//  1. Creating a task with the broker down still returns 201, and does so
//     promptly. The request path no longer contacts Kafka at all, so there is no
//     publish to fail and no timeout to wait out.
//  2. The event exists as a row with sent_at NULL. This is the part that makes
//     the outage survivable: the event is data in the same database as the task,
//     not a log line that scrolled past.
//  3. When the broker returns, the poller drains the row without anyone asking.
//
// Before the outbox, step 1 was the only one that held, and the event was gone.
func TestTaskCreatedDuringBrokerOutageIsPublishedWhenItReturns(t *testing.T) {
	base, db := requireStack(t)
	token, projectID := newProject(t, base)

	// Stop the broker, and make sure it comes back even if an assertion below
	// fails -- leaving a stopped Kafka behind would break every later test.
	compose(t, "stop", "kafka")
	kafkaRunning := false
	t.Cleanup(func() {
		if !kafkaRunning {
			compose(t, "start", "kafka")
		}
	})

	// Claim 1: the write succeeds with no broker, and is not merely slow.
	start := time.Now()
	status, body := do(t, http.MethodPost, base+"/tasks", token, map[string]any{
		"title":       "Outbox E2E Task " + uniqueSuffix(),
		"description": "Created while the broker was stopped",
		"project_id":  projectID,
		"status":      "In Progress",
		"priority":    "High",
		"estimate":    "L",
	})
	elapsed := time.Since(start)

	if status != http.StatusCreated {
		t.Fatalf("create task with the broker stopped returned %d, want 201: %v",
			status, body)
	}
	task, ok := body["task"].(map[string]any)
	if !ok {
		t.Fatalf("create task returned no task object: %v", body)
	}
	taskID, ok := task["id"].(float64)
	if !ok {
		t.Fatalf("created task has no numeric id: %v", task)
	}
	// The old code waited up to a 2s publish timeout here. Nothing in the
	// request path talks to Kafka now, so this should be milliseconds; a slow
	// create means someone reintroduced a synchronous publish.
	if elapsed > 2*time.Second {
		t.Errorf("create took %s with the broker down; the request path should "+
			"never contact Kafka", elapsed)
	}

	// Claim 2: the event is a durable row, and it is not yet sent.
	key := fmt.Sprintf("%d", int(taskID))
	var pending []outbox.Record
	if err := db.Where("key = ?", key).Find(&pending).Error; err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("found %d outbox rows for task %s, want exactly 1: the task "+
			"committed but its event was not recorded", len(pending), key)
	}
	if pending[0].SentAt != nil {
		t.Errorf("outbox row for task %s already has sent_at=%v with the broker "+
			"stopped, which should be impossible", key, pending[0].SentAt)
	}
	if pending[0].EventID == "" {
		t.Errorf("outbox row for task %s has no event_id, so the consumer could "+
			"not deduplicate a redelivery", key)
	}

	// Claim 3: bring the broker back and let the poller do its job unaided.
	compose(t, "start", "kafka")
	kafkaRunning = true

	deadline := time.Now().Add(drainTimeout)
	for {
		var row outbox.Record
		if err := db.Where("key = ?", key).First(&row).Error; err != nil {
			t.Fatalf("re-read outbox: %v", err)
		}
		if row.SentAt != nil {
			if row.Attempts < 1 {
				t.Errorf("row published with attempts=%d, want at least 1",
					row.Attempts)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("outbox row for task %s still unsent %s after the broker "+
				"came back (attempts=%d); the poller is not draining",
				key, drainTimeout, row.Attempts)
		}
		time.Sleep(time.Second)
	}
}

// The steady-state counterpart: with the broker up, a created task's event is
// published without intervention and nothing is left behind.
//
// Worth asserting separately because the outage test above would also pass if
// the poller only ever ran after a restart. This one pins down that the normal
// path drains too, and quickly.
func TestTaskCreatedWithBrokerUpDrainsPromptly(t *testing.T) {
	base, db := requireStack(t)
	token, projectID := newProject(t, base)

	status, body := do(t, http.MethodPost, base+"/tasks", token, map[string]any{
		"title":       "Outbox Steady State " + uniqueSuffix(),
		"description": "Created with the broker running",
		"project_id":  projectID,
		"status":      "Not Started",
		"priority":    "Medium",
		"estimate":    "S",
	})
	if status != http.StatusCreated {
		t.Fatalf("create task returned %d, want 201: %v", status, body)
	}
	task, ok := body["task"].(map[string]any)
	if !ok {
		t.Fatalf("create task returned no task object: %v", body)
	}
	taskID, ok := task["id"].(float64)
	if !ok {
		t.Fatalf("created task has no numeric id: %v", task)
	}

	key := fmt.Sprintf("%d", int(taskID))
	deadline := time.Now().Add(30 * time.Second)
	for {
		var row outbox.Record
		if err := db.Where("key = ?", key).First(&row).Error; err != nil {
			t.Fatalf("read outbox for task %s: %v", key, err)
		}
		if row.SentAt != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("outbox row for task %s unsent after 30s with the broker "+
				"up (attempts=%d)", key, row.Attempts)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
