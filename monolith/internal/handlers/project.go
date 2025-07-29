package handlers

import (
	"github.com/P4rz1val22/task-management-api/internal/database"
	"github.com/P4rz1val22/task-management-api/internal/models"
	"github.com/gin-gonic/gin"
	"net/http"
)

type ProjectRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

// @Summary		Create a new project
// @Description	Create a new project owned by the authenticated user
// @Tags			projects
// @Accept			json
// @Produce		json
// @Security		BearerAuth
// @Param			project	body		ProjectRequest	true	"Project creation data"
// @Success		201		{object}	map[string]interface{}
// @Failure		400		{object}	map[string]interface{}
// @Failure		401		{object}	map[string]interface{}
// @Router			/projects [post]
// @Summary    Create a new project
// @Description Create a new project owned by the authenticated user
// @Tags          projects
// @Accept        json
// @Produce    json
// @Security       BearerAuth
// @Param         project    body      ProjectRequest   true   "Project creation data"
// @Success    201       {object}   map[string]interface{}
// @Failure    400       {object}   map[string]interface{}
// @Failure    401       {object}   map[string]interface{}
// @Router        /projects [post]
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

// @Summary    Get all projects
// @Description Get list of projects owned by authenticated user
// @Tags       projects
// @Produce    json
// @Security   BearerAuth
// @Success    200   {object}    map[string]interface{}
// @Failure    401   {object}    map[string]interface{}
// @Failure    500   {object}    map[string]interface{}
// @Router     /projects [get]
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

// @Summary    Get project by ID
// @Description Get detailed information about a specific project owned by authenticated user
// @Tags       projects
// @Produce    json
// @Security   BearerAuth
// @Param      id    path    int    true    "Project ID"
// @Success    200   {object}    map[string]interface{}
// @Failure    401   {object}    map[string]interface{}
// @Failure    404   {object}    map[string]interface{}
// @Router     /projects/{id} [get]
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

// @Summary    Update project by ID
// @Description Update a specific project owned by authenticated user
// @Tags       projects
// @Accept     json
// @Produce    json
// @Security   BearerAuth
// @Param      id       path    int             true   "Project ID"
// @Param      project  body    ProjectRequest  true   "Project update data"
// @Success    200      {object} map[string]interface{}
// @Failure    400      {object} map[string]interface{}
// @Failure    401      {object} map[string]interface{}
// @Failure    404      {object} map[string]interface{}
// @Failure    409      {object} map[string]interface{}
// @Router     /projects/{id} [put]
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

// @Summary    Delete project by ID
// @Description Delete a specific project owned by authenticated user (only if no tasks exist)
// @Tags       projects
// @Security   BearerAuth
// @Param      id    path    int    true    "Project ID"
// @Success    200   {object}    map[string]interface{}
// @Failure    400   {object}    map[string]interface{}
// @Failure    401   {object}    map[string]interface{}
// @Failure    404   {object}    map[string]interface{}
// @Router     /projects/{id} [delete]
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
