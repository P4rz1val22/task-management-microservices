// Command notification-service consumes task events and reacts to them.
//
// It serves no HTTP: it is a pure consumer, which is the point. task-service
// returns 201 the moment a task is committed and the event is logged to the
// broker; whether this process is running, restarting or has been down for an
// hour makes no difference to that. When it comes back it resumes from its
// committed offset and catches up.
//
// Stage 5 logged each event; Stage 6 turns that into an email. With SMTP
// credentials unset -- the default -- it logs what it would have sent instead,
// which exercises the entire pipeline without anything leaving the machine.
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"task-management-notification-service/internal/consumer"
	"task-management-notification-service/internal/database"
	"task-management-notification-service/internal/services"
	"task-management-notification-service/internal/users"
)

func main() {
	// Cancelled on SIGINT/SIGTERM, which is what `docker compose stop` sends.
	// The loop notices, stops fetching, and the deferred Close flushes the
	// final offset commit -- without that, a restart would re-deliver events
	// that had already been handled.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := consumer.ConfigFromEnv()
	log.Printf("[NOTIFICATION-SERVICE] starting; brokers=%v topic=%q group=%q",
		cfg.Brokers, cfg.Topic, cfg.GroupID)

	handler := consumer.New()

	// Email is wired opportunistically. If Postgres is unreachable at startup
	// the service still consumes and still reports each event -- it just cannot
	// resolve an address, so it says so once instead of crash-looping. A
	// consumer that refuses to start because a side effect is unavailable would
	// repeat, on this side, the coupling this whole migration removes.
	if db, err := database.Connect(); err != nil {
		log.Printf("[NOTIFICATION-SERVICE] no database (%v); running in log-only mode", err)
	} else {
		handler.Recipients = users.NewDBLookup(db)
		handler.Mailer = services.NewEmailService()
	}

	reader := consumer.NewReader(cfg)
	defer func() {
		if err := reader.Close(); err != nil {
			log.Printf("[NOTIFICATION-SERVICE] closing reader: %v", err)
		}
	}()

	if err := consumer.Run(ctx, reader, handler); err != nil {
		// Not log.Fatal: that skips the deferred Close and abandons the offset
		// commit. Log, return, and let the deferred cleanup run.
		log.Printf("[NOTIFICATION-SERVICE] consumer stopped with error: %v", err)
		return
	}

	log.Println("[NOTIFICATION-SERVICE] stopped cleanly")
}
