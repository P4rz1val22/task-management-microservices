package main

import (
	"fmt"
	"log"
	"task-management-auth-service/internal/database"
	"task-management-auth-service/internal/handlers"

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
		return fmt.Sprintf("[AUTH-SERVICE] %s - %s %s %d %s\n",
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

	// Auth routes
	auth := r.Group("/auth")
	{
		auth.POST("/register", handlers.Register)
		auth.POST("/login", handlers.Login)
	}

	log.Println("🔐 Auth Service starting on port 8082")
	log.Println("📋 Available endpoints:")
	log.Println("   GET  /health")
	log.Println("   POST /auth/register")
	log.Println("   POST /auth/login")

	if err := r.Run(":8082"); err != nil {
		log.Fatal("Failed to start auth service:", err)
	}
}
