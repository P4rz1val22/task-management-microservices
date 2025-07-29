//	@title			Task Management API
//	@version		1.0
//	@description	A production-ready task management API with JWT authentication
//	@termsOfService	http://swagger.io/terms/

//	@contact.name	Luis Sarmiento
//	@contact.email	luis.sar.cor@gmail.com

//	@license.name	MIT
//	@license.url	https://opensource.org/licenses/MIT

//	@host		localhost:8080
//	@BasePath	/

//	@servers	http://localhost:8080	Local Development
//	@servers	https://task-management-api-production-0512.up.railway.app	Production

//	@securityDefinitions.apikey	BearerAuth
//	@in							header
//	@name						Authorization
//	@description				Type "Bearer" followed by a space and JWT token.

package main

import (
	"log"
	"net/http"

	"github.com/P4rz1val22/task-management-api/internal/database"
	"github.com/P4rz1val22/task-management-api/internal/handlers"
	"github.com/P4rz1val22/task-management-api/internal/middleware"
	"github.com/gin-gonic/gin"
)

func main() {
	database.Connect()

	r := gin.Default()

	//r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	// Health Check
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":   "healthy",
			"message":  "Task Management API is running!",
			"database": "connected",
		})
	})

	auth := r.Group("/auth")
	{
		auth.POST("/register", handlers.Register)
		auth.POST("/login", handlers.Login)
	}

	users := r.Group("/users")

	users.Use(middleware.RequireAuth())
	{
		users.GET("/me", handlers.GetCurrentUser)
		users.PUT("/me", handlers.UpdateCurrentUser)
	}

	projects := r.Group("/projects")
	projects.Use(middleware.RequireAuth())
	{
		projects.POST("", handlers.CreateProject)
		projects.GET("", handlers.GetProjects)
		projects.GET("/:id", handlers.GetProjectByID)
		projects.PUT("/:id", handlers.UpdateProject)
		projects.DELETE("/:id", handlers.DeleteProject)
	}

	tasks := r.Group("/tasks")
	tasks.Use(middleware.RequireAuth())
	{
		tasks.POST("", handlers.CreateTask)
		tasks.GET("", handlers.GetTasks)
		tasks.GET("/:id", handlers.GetTaskByID)
		tasks.PUT("/:id", handlers.UpdateTask)
		tasks.DELETE("/:id", handlers.DeleteTask)
	}

	// Start server on port 8080
	log.Println("Starting server on :8080...")
	if err := r.Run(":8080"); err != nil {
		log.Fatal("Failed to start server:", err)
	}
}
