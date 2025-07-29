package handlers

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"task-management-project-service/internal/database"
	"task-management-project-service/internal/models"
)

type ProjectRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

// CreateProject handles project creation
func CreateProject(c *gin.Context) {
	userID := c.GetUint("user_id")

	var req ProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var existingProject models.Project
	if err := database.DB.Where("name = ? AND owner_id = ?", req.Name, userID).First(&existingProject).Error; err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Project already exists"})
		return
	}

	// Create a project
	project := models.Project{
		Name:        req.Name,
		Description: req.Description,
		OwnerID:     userID,
	}

	if err := database.DB.Create(&project).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create project"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message": "Project created successfully",
		"project": gin.H{
			"id":          project.ID,
			"name":        project.Name,
			"description": project.Description,
			"owner_id":    project.OwnerID,
			"owner":       "You",
			"created_at":  project.CreatedAt,
		},
	})
}

// GetProjects handles listing all projects for authenticated user
func GetProjects(c *gin.Context) {
	userID := c.GetUint("user_id")

	var projects []models.Project
	if err := database.DB.Preload("Owner").Where("owner_id = ?", userID).Find(&projects).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch projects"})
		return
	}

	var projectList []gin.H
	for _, project := range projects {
		projectList = append(projectList, gin.H{
			"id":          project.ID,
			"name":        project.Name,
			"description": project.Description,
			"owner_id":    project.OwnerID,
			"owner":       project.Owner.Name,
			"created_at":  project.CreatedAt,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"projects": projectList,
	})
}

// GetProjectByID handles getting a specific project
func GetProjectByID(c *gin.Context) {
	userID := c.GetUint("user_id")

	projectID := c.Param("id")

	var project models.Project
	if err := database.DB.Preload("Owner").Preload("Tasks").Where("id = ? AND owner_id = ?", projectID, userID).First(&project).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"project": gin.H{
			"id":          project.ID,
			"name":        project.Name,
			"description": project.Description,
			"owner_id":    project.OwnerID,
			"owner":       project.Owner.Name,
			"task_count":  len(project.Tasks),
			"created_at":  project.CreatedAt,
			"updated_at":  project.UpdatedAt,
		},
	})
}

// UpdateProject handles project updates
func UpdateProject(c *gin.Context) {
	userID := c.GetUint("user_id")
	projectID := c.Param("id")

	var req ProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var project models.Project
	if err := database.DB.Where("id = ? AND owner_id = ?", projectID, userID).First(&project).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project not found"})
		return
	}

	var existingProject models.Project
	if err := database.DB.Where("name = ? AND owner_id = ? AND id != ?", req.Name, userID, projectID).First(&existingProject).Error; err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Project name already exists"})
		return
	}

	project.Name = req.Name
	project.Description = req.Description

	if err := database.DB.Save(&project).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update project"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Project updated successfully",
		"project": gin.H{
			"id":          project.ID,
			"name":        project.Name,
			"description": project.Description,
			"updated_at":  project.UpdatedAt,
		},
	})
}

// DeleteProject handles project deletion
func DeleteProject(c *gin.Context) {
	userID := c.GetUint("user_id")
	projectID := c.Param("id")

	// Find project and verify ownership
	var project models.Project
	if err := database.DB.Where("id = ? AND owner_id = ?", projectID, userID).First(&project).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project not found"})
		return
	}

	// Check for existing tasks
	var taskCount int64
	database.DB.Model(&models.Task{}).Where("project_id = ?", projectID).Count(&taskCount)
	if taskCount > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":      "Cannot delete project with existing tasks. Please delete or move all tasks first.",
			"task_count": taskCount,
		})
		return
	}

	// Safe to delete - no tasks exist
	if err := database.DB.Delete(&project).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete project"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Project deleted successfully",
	})
}

// HealthCheck for project service
func HealthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"service": "project-service",
		"status":  "healthy",
		"port":    "8083",
		"message": "Project management service is running",
	})
}
