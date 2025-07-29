package database

import (
	"log"
	"os"

	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"task-management-task-service/internal/models"
)

var DB *gorm.DB

func Connect() {
	// Load environment variables
	err := godotenv.Load("../.env")
	if err != nil {
		log.Println("Warning: .env file not found")
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}

	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}

	// Ensure our tables exist (should already be there from monolith)
	err = DB.AutoMigrate(&models.User{}, &models.Project{}, &models.Task{})
	if err != nil {
		log.Fatal("Failed to migrate database:", err)
	}

	log.Println("Task Service: Database connected successfully!")
}
