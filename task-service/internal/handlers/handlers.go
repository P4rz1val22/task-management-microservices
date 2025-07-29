package handlers

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"task-management-task-service/internal/database"
	"task-management-task-service/internal/models"
	"time"
)

type TaskRequest struct {
	Title       string `json:"title" binding:"required"`
	Description string `json:"description"`
	ProjectID   uint   `json:"project_id" binding:"required"`
	Priority    string `json:"priority"`
	Estimate    string `json:"estimate"`
	Status      string `json:"status"`
	DueDate     string `json:"due_date"`
}

// Validation functions (same as monolith)
func isValidStatus(status string) bool {
	validStatuses := []string{"Not Started", "In Progress", "Done", "Blocked"}
	for _, v := range validStatuses {
		if v == status {
			return true
		}
	}
	return false
}

func isValidPriority(priority string) bool {
	validPriorities := []string{"Low", "Medium", "High", "Urgent"}
	for _, v := range validPriorities {
		if v == priority {
			return true
		}
	}
	return false
}

func isValidEstimate(estimate string) bool {
	validEstimates := []string{"S", "M", "L", "XL"}
	for _, v := range validEstimates {
		if v == estimate {
			return true
		}
	}
	return false
}

// CreateTask handles task creation
func CreateTask(c *gin.Context) {
	userID := c.GetUint("user_id")
	var req TaskRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Verify project ownership
	var project models.Project
	if err := database.DB.Where("id = ? AND owner_id = ?", req.ProjectID, userID).First(&project).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project not found or access denied"})
		return
	}

	// Validate optional fields
	if req.Status != "" && !isValidStatus(req.Status) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid status. Use: Not Started, In Progress, Done, Blocked"})
		return
	}
	if req.Priority != "" && !isValidPriority(req.Priority) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid priority. Use: Low, Medium, High, Urgent"})
		return
	}
	if req.Estimate != "" && !isValidEstimate(req.Estimate) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid estimate. Use: S, M, L, XL"})
		return
	}

	// Parse due date if provided
	var dueDate *time.Time
	if req.DueDate != "" {
		parsed, err := time.Parse("2006-01-02", req.DueDate)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid due_date format. Use YYYY-MM-DD"})
			return
		}
		dueDate = &parsed
	}

	// Create task
	task := models.Task{
		Title:       req.Title,
		Description: req.Description,
		ProjectID:   req.ProjectID,
		AssigneeID:  &userID,
		CreatorID:   &userID,
		Status:      req.Status,
		Estimate:    req.Estimate,
		Priority:    req.Priority,
		DueDate:     dueDate,
	}

	if err := database.DB.Create(&task).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create task"})
		return
	}

	// TODO: Send notification to Notification Service (future microservice)
	// For now, we skip the email notification

	c.JSON(http.StatusCreated, gin.H{
		"message": "Task created successfully",
		"task": gin.H{
			"id":          task.ID,
			"title":       task.Title,
			"description": task.Description,
			"project_id":  task.ProjectID,
			"status":      task.Status,
			"estimate":    task.Estimate,
			"priority":    task.Priority,
			"due_date":    task.DueDate,
			"created_at":  task.CreatedAt,
		},
	})
}

// GetTasks handles complex task filtering and listing
func GetTasks(c *gin.Context) {
	userID := c.GetUint("user_id")

	// Base query with preloading for enriched data
	query := database.DB.Preload("Project").Preload("Creator").Preload("Assignee").Where("assignee_id = ?", userID)

	// Filter by project_id
	if projectID := c.Query("project_id"); projectID != "" {
		var project models.Project
		if err := database.DB.Where("id = ? AND owner_id = ?", projectID, userID).First(&project).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Project not found or access denied"})
			return
		}
		query = query.Where("project_id = ?", projectID)
	}

	// Filter by status
	if status := c.Query("status"); status != "" {
		if !isValidStatus(status) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid status. Use: Not Started, In Progress, Done, Blocked"})
			return
		}
		query = query.Where("status = ?", status)
	}

	// Filter by priority
	if priority := c.Query("priority"); priority != "" {
		if !isValidPriority(priority) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid priority. Use: Low, Medium, High, Urgent"})
			return
		}
		query = query.Where("priority = ?", priority)
	}

	// Filter by estimate
	if estimate := c.Query("estimate"); estimate != "" {
		if !isValidEstimate(estimate) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid estimate. Use: S, M, L, XL"})
			return
		}
		query = query.Where("estimate = ?", estimate)
	}

	// Filter by due date range
	if dueDateFrom := c.Query("due_date_from"); dueDateFrom != "" {
		date, err := time.Parse("2006-01-02", dueDateFrom)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid due_date_from format. Use YYYY-MM-DD"})
			return
		}
		query = query.Where("due_date >= ?", date)
	}
	if dueDateTo := c.Query("due_date_to"); dueDateTo != "" {
		date, err := time.Parse("2006-01-02", dueDateTo)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid due_date_to format. Use YYYY-MM-DD"})
			return
		}
		query = query.Where("due_date <= ?", date)
	}

	// Execute query
	var tasks []models.Task
	if err := query.Find(&tasks).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch tasks"})
		return
	}

	// Build enriched response with cross-service data
	var taskList []gin.H
	for _, task := range tasks {
		taskList = append(taskList, gin.H{
			"id":          task.ID,
			"title":       task.Title,
			"description": task.Description,
			"project": gin.H{
				"id":   task.Project.ID,
				"name": task.Project.Name,
			},
			"creator":    task.Creator.Name,
			"assignee":   task.Assignee.Name,
			"status":     task.Status,
			"priority":   task.Priority,
			"estimate":   task.Estimate,
			"due_date":   task.DueDate,
			"created_at": task.CreatedAt,
			"updated_at": task.UpdatedAt,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"tasks": taskList,
	})
}

// GetTaskByID handles individual task retrieval with authorization
func GetTaskByID(c *gin.Context) {
	userID := c.GetUint("user_id")
	taskID := c.Param("id")

	var task models.Task
	if err := database.DB.Preload("Project").Preload("Creator").Preload("Assignee").Where("id = ? AND assignee_id = ?", taskID, userID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	// Additional authorization - verify project ownership
	if task.Project.OwnerID != userID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"task": gin.H{
			"id":          task.ID,
			"title":       task.Title,
			"description": task.Description,
			"project": gin.H{
				"id":   task.Project.ID,
				"name": task.Project.Name,
			},
			"creator":    task.Creator.Name,
			"assignee":   task.Assignee.Name,
			"status":     task.Status,
			"priority":   task.Priority,
			"estimate":   task.Estimate,
			"due_date":   task.DueDate,
			"created_at": task.CreatedAt,
			"updated_at": task.UpdatedAt,
		},
	})
}

// UpdateTask handles task updates with change tracking
func UpdateTask(c *gin.Context) {
	userID := c.GetUint("user_id")
	taskID := c.Param("id")

	var req TaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Find and authorize task
	var task models.Task
	if err := database.DB.Preload("Project").Where("id = ? AND assignee_id = ?", taskID, userID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	if task.Project.OwnerID != userID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	// Validate project change if requested
	if req.ProjectID != task.ProjectID {
		var newProject models.Project
		if err := database.DB.Where("id = ? AND owner_id = ?", req.ProjectID, userID).First(&newProject).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Target project not found or access denied"})
			return
		}
	}

	// Validate optional fields
	if req.Status != "" && !isValidStatus(req.Status) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid status. Use: Not Started, In Progress, Done, Blocked"})
		return
	}
	if req.Priority != "" && !isValidPriority(req.Priority) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid priority. Use: Low, Medium, High, Urgent"})
		return
	}
	if req.Estimate != "" && !isValidEstimate(req.Estimate) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid estimate. Use: S, M, L, XL"})
		return
	}

	// Parse due date
	var dueDate *time.Time
	if req.DueDate != "" {
		parsed, err := time.Parse("2006-01-02", req.DueDate)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid due_date format. Use YYYY-MM-DD"})
			return
		}
		dueDate = &parsed
	}

	// Track changes for notification (future use)
	//originalTitle := task.Title
	//originalStatus := task.Status
	//originalPriority := task.Priority
	//originalEstimate := task.Estimate

	// Update task
	task.Title = req.Title
	task.Description = req.Description
	task.ProjectID = req.ProjectID
	task.Status = req.Status
	task.Priority = req.Priority
	task.Estimate = req.Estimate
	task.DueDate = dueDate

	if err := database.DB.Save(&task).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update task"})
		return
	}

	// TODO: Send change notification to Notification Service
	// Track what changed: originalTitle vs task.Title, etc.

	c.JSON(http.StatusOK, gin.H{
		"message": "Task updated successfully",
		"task": gin.H{
			"id":          task.ID,
			"title":       task.Title,
			"description": task.Description,
			"project_id":  task.ProjectID,
			"status":      task.Status,
			"priority":    task.Priority,
			"estimate":    task.Estimate,
			"due_date":    task.DueDate,
			"updated_at":  task.UpdatedAt,
		},
	})
}

// DeleteTask handles task deletion with authorization
func DeleteTask(c *gin.Context) {
	userID := c.GetUint("user_id")
	taskID := c.Param("id")

	var task models.Task
	if err := database.DB.Preload("Project").Where("id = ? AND assignee_id = ?", taskID, userID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	if task.Project.OwnerID != userID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	if err := database.DB.Delete(&task).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete task"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Task deleted successfully",
	})
}

// HealthCheck for task service
func HealthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"service": "task-service",
		"status":  "healthy",
		"port":    "8084",
		"message": "Task management service is running",
	})
}
