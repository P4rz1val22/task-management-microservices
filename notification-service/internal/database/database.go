// Package database opens the shared Postgres connection.
//
// Deliberately smaller than its equivalents in the other four services: it does
// NOT call AutoMigrate. Those four all race to create the same tables the
// instant Postgres reports healthy, and two of them collide on CREATE TYPE --
// the known flapping documented in CLAUDE.md. This service only ever reads one
// column from one table that already exists, so joining that race would add a
// fifth contender and fix nothing.
package database

import (
	"fmt"
	"os"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Connect opens the pool. It returns an error rather than calling log.Fatal, so
// main can decide -- and main decides to carry on without email rather than
// crash-loop, because a consumer that cannot look up addresses can still read
// the topic and report what it would have sent.
func Connect() (*gorm.DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL is not set")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		// Quiet: this service issues one small query per event, and logging
		// each one would bury the notification lines that matter.
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	return db, nil
}
