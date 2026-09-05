package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"task-management-task-service/internal/database"
	"task-management-task-service/internal/events"
	"task-management-task-service/internal/handlers"
	"task-management-task-service/internal/middleware"
	"task-management-task-service/internal/outbox"
)

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func main() {
	// Connect to database
	database.Connect()

	// The outbox table is task-service's own -- nothing else reads or writes it
	// -- so migrating it here is not the four-way race the shared tables have.
	if err := outbox.Migrate(database.DB); err != nil {
		log.Fatal("Failed to migrate outbox:", err)
	}

	// Handlers no longer publish. They record events into the outbox inside the
	// same transaction as the change, and this poller drains that table in the
	// background. The request path never touches Kafka, so a broker outage
	// cannot slow a write down, let alone lose its event.
	handlers.OutboxTopic = valueOr(os.Getenv("KAFKA_TOPIC"), events.DefaultTopic)

	publisher := events.NewPublisher()
	defer publisher.Close()

	pollerCtx, stopPoller := context.WithCancel(context.Background())
	defer stopPoller()
	go outbox.NewPoller(database.DB, publisher).Run(pollerCtx)

	// Set Gin mode
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()

	// Add logging middleware
	r.Use(gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		return fmt.Sprintf("[TASK-SERVICE] %s - %s %s %d %s\n",
			param.TimeStamp.Format("15:04:05"),
			param.Method,
			param.Path,
			param.StatusCode,
			param.Latency,
		)
	}))

	// Add recovery middleware
	r.Use(gin.Recovery())

	// Health check
	r.GET("/health", handlers.HealthCheck)

	// Task routes (all protected)
	tasks := r.Group("/tasks")
	tasks.Use(middleware.RequireAuth())
	{
		tasks.POST("", handlers.CreateTask)
		tasks.GET("", handlers.GetTasks)
		tasks.GET("/:id", handlers.GetTaskByID)
		tasks.PUT("/:id", handlers.UpdateTask)
		tasks.DELETE("/:id", handlers.DeleteTask)
	}

	log.Println("📋 Task Service starting on port 8084")
	log.Println("📋 Available endpoints:")
	log.Println("   GET  /health")
	log.Println("   POST /tasks")
	log.Println("   GET  /tasks (with filtering: ?project_id=X&status=Y&priority=Z)")
	log.Println("   GET  /tasks/:id")
	log.Println("   PUT  /tasks/:id")
	log.Println("   DELETE /tasks/:id")

	if err := r.Run(":8084"); err != nil {
		log.Fatal("Failed to start task service:", err)
	}
}
