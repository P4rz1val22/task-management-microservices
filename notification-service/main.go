// Command notification-service consumes task events and reacts to them.
//
// It serves no HTTP: it is a pure consumer, which is the point. task-service
// returns 201 the moment a task is committed and the event is logged to the
// broker; whether this process is running, restarting or has been down for an
// hour makes no difference to that. When it comes back it resumes from its
// committed offset and catches up.
//
// Stage 5 logs each event. Stage 6 turns that log line into an email.
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"task-management-notification-service/internal/consumer"
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

	reader := consumer.NewReader(cfg)
	defer func() {
		if err := reader.Close(); err != nil {
			log.Printf("[NOTIFICATION-SERVICE] closing reader: %v", err)
		}
	}()

	if err := consumer.Run(ctx, reader, consumer.New()); err != nil {
		// Not log.Fatal: that skips the deferred Close and abandons the offset
		// commit. Log, return, and let the deferred cleanup run.
		log.Printf("[NOTIFICATION-SERVICE] consumer stopped with error: %v", err)
		return
	}

	log.Println("[NOTIFICATION-SERVICE] stopped cleanly")
}
