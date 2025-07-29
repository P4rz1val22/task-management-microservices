package main

import (
	"fmt"
	"log"
	"task-management-project-service/internal/database"
	"task-management-project-service/internal/handlers"
	"task-management-project-service/internal/middleware"

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
		return fmt.Sprintf("[PROJECT-SERVICE] %s - %s %s %d %s\n",
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

	// Project routes (all protected)
	projects := r.Group("/projects")
	projects.Use(middleware.RequireAuth())
	{
		projects.POST("", handlers.CreateProject)
		projects.GET("", handlers.GetProjects)
		projects.GET("/:id", handlers.GetProjectByID)
		projects.PUT("/:id", handlers.UpdateProject)
		projects.DELETE("/:id", handlers.DeleteProject)
	}

	log.Println("🗂️  Project Service starting on port 8083")
	log.Println("📋 Available endpoints:")
	log.Println("   GET  /health")
	log.Println("   POST /projects")
	log.Println("   GET  /projects")
	log.Println("   GET  /projects/:id")
	log.Println("   PUT  /projects/:id")
	log.Println("   DELETE /projects/:id")

	if err := r.Run(":8083"); err != nil {
		log.Fatal("Failed to start project service:", err)
	}
}
