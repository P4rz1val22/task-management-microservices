//go:build integration

// End-to-end coverage for the API Gateway, driven over HTTP like a real client.
//
// This is the first Go test in the gateway module, and it covers the seam the
// unit suites structurally cannot reach. Every other Go test in this repo runs
// against a mock database inside one service; nothing exercised the gateway, the
// JWT middleware, or a request that crosses a service boundary. Those are the
// parts of a Strangler Fig migration most likely to break, because they are the
// parts that only exist once the whole stack is assembled.
//
// Behind a build tag on purpose: the default `go test ./...` must stay fast and
// need no Docker, so this never runs unless asked for explicitly.
//
//	docker compose up -d
//	cd gateway && go test -tags=integration ./internal/e2e/
//
// Everything here talks to localhost:8081, the gateway's HOST port. It uses only
// the standard library: a proxy has no business growing a database driver to
// satisfy a test.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"testing"
	"time"
)

// defaultGatewayURL is the host-side address. Containers reach each other by
// service name on the compose network; a host process must use the published
// port.
const defaultGatewayURL = "http://localhost:8081"

func gatewayURL() string {
	if u := os.Getenv("GATEWAY_URL"); u != "" {
		return u
	}
	return defaultGatewayURL
}

// requireGateway fails fast with a usable message rather than letting every
// assertion below fail one at a time, if the stack is not up.
func requireGateway(t *testing.T) string {
	t.Helper()
	base := gatewayURL()

	// /gateway/health, not /health: the gateway does not serve /health at all.
	// That path falls through to r.NoRoute(proxy.SmartProxy()) and is answered by
	// the monolith, so it returns a healthy-looking 200 that says nothing about
	// whether the gateway is up.
	resp, err := http.Get(base + "/gateway/health")
	if err != nil {
		t.Fatalf("no gateway at %s (is `docker compose up -d` running?): %v", base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("gateway health at %s returned %d, want 200", base, resp.StatusCode)
	}
	return base
}

// uniqueSuffix keeps the test re-runnable against a database it has already
// touched. Emails and project names are unique-constrained, so hardcoding them
// makes the second run fail on a conflict -- the exact fault that made the
// Postman collection untrustworthy.
func uniqueSuffix() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), rand.Intn(1000))
}

// do sends an optionally-authenticated JSON request and decodes the response.
// It returns the status code so callers can assert on it rather than fataling
// inside the helper, which would hide which step actually broke.
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

// register creates a fresh user through the gateway and returns its JWT.
//
// Registering rather than reusing a fixture user is deliberate: it means the
// token is minted by auth-service during this run, so the test proves the
// gateway routes /auth/* correctly instead of assuming a token from elsewhere.
func register(t *testing.T, base string) string {
	t.Helper()

	status, body := do(t, http.MethodPost, base+"/auth/register", "", map[string]any{
		"name":     "Gateway E2E",
		"email":    "gateway-e2e-" + uniqueSuffix() + "@test.com",
		"password": "password123",
	})
	if status != http.StatusCreated {
		t.Fatalf("register through gateway returned %d, want 201: %v", status, body)
	}

	token, ok := body["token"].(string)
	if !ok || token == "" {
		t.Fatalf("register returned no token: %v", body)
	}
	return token
}

// The headline claim: one request chain, four services, through the gateway.
//
// Register hits auth-service, create-project hits project-service, create-task
// hits task-service, and reading the task back proves task-service called
// project-service to enrich the response. No unit test can make that claim,
// because each one runs a single service against a mock database.
func TestGatewayRoutesFullTaskFlow(t *testing.T) {
	base := requireGateway(t)
	token := register(t, base)

	projectName := "Gateway E2E Project " + uniqueSuffix()
	status, body := do(t, http.MethodPost, base+"/projects", token, map[string]any{
		"name":        projectName,
		"description": "Created by the gateway integration test",
	})
	if status != http.StatusCreated {
		t.Fatalf("create project through gateway returned %d, want 201: %v", status, body)
	}
	project, ok := body["project"].(map[string]any)
	if !ok {
		t.Fatalf("create project returned no project object: %v", body)
	}
	projectID, ok := project["id"].(float64)
	if !ok {
		t.Fatalf("created project has no numeric id: %v", project)
	}

	taskTitle := "Gateway E2E Task " + uniqueSuffix()
	status, body = do(t, http.MethodPost, base+"/tasks", token, map[string]any{
		"title":       taskTitle,
		"description": "Created by the gateway integration test",
		"project_id":  projectID,
		"status":      "In Progress",
		"priority":    "High",
		"estimate":    "L",
	})
	if status != http.StatusCreated {
		t.Fatalf("create task through gateway returned %d, want 201: %v", status, body)
	}
	task, ok := body["task"].(map[string]any)
	if !ok {
		t.Fatalf("create task returned no task object: %v", body)
	}
	taskID, ok := task["id"].(float64)
	if !ok {
		t.Fatalf("created task has no numeric id: %v", task)
	}

	// Read it back. This is the assertion that earns the test: the enriched
	// project name can only be present if task-service resolved it from
	// project-service, so a broken cross-service call fails here rather than
	// silently returning a bare task.
	status, body = do(t, http.MethodGet,
		fmt.Sprintf("%s/tasks/%d", base, int(taskID)), token, nil)
	if status != http.StatusOK {
		t.Fatalf("read task back through gateway returned %d, want 200: %v", status, body)
	}
	task, ok = body["task"].(map[string]any)
	if !ok {
		t.Fatalf("read task returned no task object: %v", body)
	}
	if got := task["title"]; got != taskTitle {
		t.Errorf("task title = %v, want %q", got, taskTitle)
	}

	enriched, ok := task["project"].(map[string]any)
	if !ok {
		t.Fatalf("task came back without an enriched project object, so "+
			"task-service did not reach project-service: %v", task)
	}
	if got := enriched["name"]; got != projectName {
		t.Errorf("enriched project name = %v, want %q; the gateway routed the "+
			"task read, but the cross-service lookup returned the wrong project",
			got, projectName)
	}
}

// The JWT middleware, which no other Go test touches.
//
// Table-driven because the interesting cases are the shapes of a bad token, and
// they must all be rejected identically. A proxy that forwards an unauthenticated
// request and lets the downstream service decide is a proxy with an auth bypass.
func TestGatewayRejectsBadCredentials(t *testing.T) {
	base := requireGateway(t)

	tests := []struct {
		name   string
		header string
	}{
		{name: "no authorization header", header: ""},
		{name: "bearer with garbage token", header: "Bearer not-a-jwt"},
		{name: "well-formed jwt with a forged signature", header: "Bearer " +
			"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
			"eyJ1c2VyX2lkIjoxLCJleHAiOjk5OTk5OTk5OTl9.forged"},
		{name: "token without the bearer scheme", header: "not-a-jwt"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, base+"/tasks", nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("GET /tasks: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusUnauthorized {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("GET /tasks returned %d, want 401: %s", resp.StatusCode, body)
			}
		})
	}
}

// /gateway/health must report on its dependencies, not just on itself.
//
// The trap this pins down: the endpoint returns HTTP 200 even when every
// dependency is unreachable, and its own gateway_status field is a hardcoded
// literal that can never report a problem. So asserting the status code -- or
// gateway_status -- proves nothing. The four downstream *_status fields are the
// only part of the response that carries information.
func TestGatewayHealthReportsEveryDependency(t *testing.T) {
	base := requireGateway(t)

	status, body := do(t, http.MethodGet, base+"/gateway/health", "", nil)
	if status != http.StatusOK {
		t.Fatalf("gateway health returned %d, want 200: %v", status, body)
	}

	for _, field := range []string{
		"monolith_status",
		"auth_service_status",
		"project_service_status",
		"task_service_status",
	} {
		if got := body[field]; got != "healthy" {
			t.Errorf("%s = %v, want \"healthy\"", field, got)
		}
	}
}
