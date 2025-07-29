package main

import (
	"fmt"
	"log"
	"task-management-gateway/internal/proxy"

	"github.com/gin-gonic/gin"
)

func main() {
	// Set Gin to release mode for cleaner output
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()

	// Add logging middleware
	r.Use(gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		return fmt.Sprintf("[GATEWAY] %s - %s %s %d %s\n",
			param.TimeStamp.Format("15:04:05"),
			param.Method,
			param.Path,
			param.StatusCode,
			param.Latency,
		)
	}))

	// Add recovery middleware
	r.Use(gin.Recovery())

	// Gateway health check (different from monolith health check)
	r.GET("/gateway/health", proxy.HealthCheck)

	// Forward requests with smart routing
	r.Any("/auth/*path", proxy.SmartProxy())
	r.Any("/projects/*path", proxy.SmartProxy())
	r.Any("/tasks/*path", proxy.SmartProxy())

	log.Println("🚀 API Gateway starting on port 8081")
	log.Println("📡 Forwarding all requests to monolith at http://localhost:8080")
	log.Println("🔍 Gateway health check: http://localhost:8081/gateway/health")

	if err := r.Run(":8081"); err != nil {
		log.Fatal("Failed to start gateway:", err)
	}
}
