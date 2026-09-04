# Task Management Microservices
#
# There is no root Go module, so `go test ./...` from here does nothing at all --
# it matches no packages and exits 0, which is worse than failing. Every Go
# command has to run from inside one of the six service directories, so these
# targets loop.
#
#   make test              unit tests, all six modules, no Docker needed
#   make test-integration   integration tests, needs the stack up
#   make test-api           the Postman collection via newman, needs the stack up
#   make test-all           all three tiers

MODULES := monolith auth-service project-service task-service gateway \
           notification-service

COLLECTION := docs/postman/Task Management Microservices API.postman_collection.json

.PHONY: test test-integration test-api test-all up down help

## The fast loop. Runs against mock databases, needs nothing running.
test:
	@for m in $(MODULES); do \
		echo "==> $$m"; \
		(cd $$m && go test ./...) || exit 1; \
	done

## Integration tests, behind the `integration` build tag so the fast loop stays
## fast. Requires `docker compose up -d`.
##
## -p 1 is not optional. Go runs separate packages in parallel, and
## task-service/internal/e2e stops the Kafka container to prove the outbox
## survives a broker outage. Without -p 1 that stops the broker out from under
## the publisher tests in internal/events, which then fail for a reason that has
## nothing to do with them.
test-integration:
	@for m in $(MODULES); do \
		echo "==> $$m (integration)"; \
		(cd $$m && go test -tags=integration -p 1 -timeout 10m ./...) || exit 1; \
	done

## API-level tests. newman is not installed globally; npx fetches it.
## The collection is idempotent, so this does not need a fresh database.
test-api:
	npx newman run "$(COLLECTION)"

test-all: test test-integration test-api

up:
	docker compose up --build -d

down:
	docker compose down

help:
	@grep -E '^[a-z-]+:' Makefile | cut -d: -f1
