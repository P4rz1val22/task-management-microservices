package main

import (
	"fmt"
	"log"
	"task-management-task-service/internal/database"
	"task-management-task-service/internal/handlers"
	"task-management-task-service/internal/middleware"

	"github.com/gin-gonic/gin"
)

func main() {
	// Connect to database
	database.Connect()

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
