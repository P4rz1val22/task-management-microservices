package handlers

import (
	"github.com/P4rz1val22/task-management-api/internal/database"
	"github.com/P4rz1val22/task-management-api/internal/models"
	"github.com/P4rz1val22/task-management-api/internal/services"
	"github.com/gin-gonic/gin"
	"net/http"
	"time"
)

var emailService = services.NewEmailService()

type TaskRequest struct {
	Title       string `json:"title" binding:"required"`
	Description string `json:"description"`
	ProjectID   uint   `json:"project_id" binding:"required"`
	Priority    string `json:"priority"`
	Estimate    string `json:"estimate"`
	Status      string `json:"status"`
	DueDate     string `json:"due_date"`
}

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

// @Summary    Create a new task
// @Description Create a new task in a project owned by the authenticated user
// @Tags       tasks
// @Accept     json
// @Produce    json
// @Security   BearerAuth
// @Param      task  body      TaskRequest  true  "Task creation data"
// @Success    201   {object}  map[string]interface{}
// @Failure    400   {object}  map[string]interface{}
// @Failure    401   {object}  map[string]interface{}
// @Failure    404   {object}  map[string]interface{}
// @Router     /tasks [post]
func CreateTask(c *gin.Context) {
	userID := c.GetUint("user_id")
	var req TaskRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var project models.Project
	if err := database.DB.Where("id = ? AND owner_id = ?", req.ProjectID, userID).First(&project).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Project not found or access denied"})
		return
	}

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

	var dueDate *time.Time
	if req.DueDate != "" {
		parsed, err := time.Parse("2006-01-02", req.DueDate)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid due_date format. Use YYYY-MM-DD"})
			return
		}
		dueDate = &parsed
	}

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

	go func() {
		// Get user email for notification
		var user models.User
		if err := database.DB.First(&user, userID).Error; err == nil {
			emailService.SendTaskCreatedNotification(task, user.Email)
		}
	}()

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

// @Summary    Get all tasks
// @Description Get list of tasks assigned to authenticated user with optional filtering
// @Tags       tasks
// @Produce    json
// @Security   BearerAuth
// @Param      project_id    query    int     false  "Filter by project ID"
// @Param      status        query    string  false  "Filter by status"
// @Param      priority      query    string  false  "Filter by priority"
// @Param      estimate      query    string  false  "Filter by estimate"
// @Param      due_date_from query    string  false  "Filter tasks due after date (YYYY-MM-DD)"
// @Param      due_date_to   query    string  false  "Filter tasks due before date (YYYY-MM-DD)"
// @Success    200           {object} map[string]interface{}
// @Failure    400           {object} map[string]interface{}
// @Failure    401           {object} map[string]interface{}
// @Router     /tasks [get]
func GetTasks(c *gin.Context) {
	userID := c.GetUint("user_id")

	query := database.DB.Preload("Project").Preload("Creator").Preload("Assignee").Where("assignee_id = ?", userID)

	if projectID := c.Query("project_id"); projectID != "" {
		var project models.Project
		if err := database.DB.Where("id = ? AND owner_id = ?", projectID, userID).First(&project).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Project not found or access denied"})
			return
		}
		query = query.Where("project_id = ?", projectID)
	}

	if status := c.Query("status"); status != "" {
		if !isValidStatus(status) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid status. Use: Not Started, In Progress, Done, Blocked"})
			return
		}
		query = query.Where("status = ?", status)
	}

	if priority := c.Query("priority"); priority != "" {
		if !isValidPriority(priority) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid priority. Use: Low, Medium, High, Urgent"})
			return
		}
		query = query.Where("priority = ?", priority)
	}

	if estimate := c.Query("estimate"); estimate != "" {
		if !isValidEstimate(estimate) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid estimate. Use: S, M, L, XL"})
			return
		}
		query = query.Where("estimate = ?", estimate)
	}

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

	var tasks []models.Task
	if err := query.Find(&tasks).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch tasks"})
		return
	}

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

// @Summary    Get task by ID
// @Description Get detailed information about a specific task assigned to authenticated user
// @Tags       tasks
// @Produce    json
// @Security   BearerAuth
// @Param      id    path    int    true    "Task ID"
// @Success    200   {object}    map[string]interface{}
// @Failure    401   {object}    map[string]interface{}
// @Failure    404   {object}    map[string]interface{}
// @Router     /tasks/{id} [get]
func GetTaskByID(c *gin.Context) {
	userID := c.GetUint("user_id")
	taskID := c.Param("id")

	var task models.Task
	if err := database.DB.Preload("Project").Preload("Creator").Preload("Assignee").Where("id = ? AND assignee_id = ?", taskID, userID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

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

// @Summary    Update task by ID
// @Description Update a specific task in a project owned by authenticated user
// @Tags       tasks
// @Accept     json
// @Produce    json
// @Security   BearerAuth
// @Param      id      path    int          true   "Task ID"
// @Param      task    body    TaskRequest  true   "Task update data"
// @Success    200     {object} map[string]interface{}
// @Failure    400     {object} map[string]interface{}
// @Failure    401     {object} map[string]interface{}
// @Failure    404     {object} map[string]interface{}
// @Router     /tasks/{id} [put]
func UpdateTask(c *gin.Context) {
	userID := c.GetUint("user_id")
	taskID := c.Param("id")

	var req TaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var task models.Task
	if err := database.DB.Preload("Project").Where("id = ? AND assignee_id = ?", taskID, userID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	if task.Project.OwnerID != userID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	if req.ProjectID != task.ProjectID {
		var newProject models.Project
		if err := database.DB.Where("id = ? AND owner_id = ?", req.ProjectID, userID).First(&newProject).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Target project not found or access denied"})
			return
		}
	}

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

	var dueDate *time.Time
	if req.DueDate != "" {
		parsed, err := time.Parse("2006-01-02", req.DueDate)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid due_date format. Use YYYY-MM-DD"})
			return
		}
		dueDate = &parsed
	}

	originalTitle := task.Title
	originalStatus := task.Status
	originalPriority := task.Priority
	originalEstimate := task.Estimate

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

	// In your UpdateTask function, replace the goroutine section:
	go func() {
		var user models.User
		if err := database.DB.First(&user, userID).Error; err == nil {
			var changes []services.ChangeDetail
			if originalTitle != task.Title {
				changes = append(changes, services.ChangeDetail{
					Field: "Title",
					From:  originalTitle,
					To:    task.Title,
				})
			}
			if originalStatus != task.Status {
				changes = append(changes, services.ChangeDetail{
					Field: "Status",
					From:  originalStatus,
					To:    task.Status,
				})
			}
			if originalPriority != task.Priority {
				changes = append(changes, services.ChangeDetail{
					Field: "Priority",
					From:  originalPriority,
					To:    task.Priority,
				})
			}

			if originalEstimate != task.Estimate {
				changes = append(changes, services.ChangeDetail{
					Field: "Estimate",
					From:  originalEstimate,
					To:    task.Estimate,
				})
			}

			emailService.SendTaskUpdatedNotification(task, user.Email, changes)
		}
	}()

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

// @Summary    Delete task by ID
// @Description Delete a specific task in a project owned by authenticated user
// @Tags       tasks
// @Security   BearerAuth
// @Param      id    path    int    true    "Task ID"
// @Success    200   {object}    map[string]interface{}
// @Failure    401   {object}    map[string]interface{}
// @Failure    404   {object}    map[string]interface{}
// @Router     /tasks/{id} [delete]
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
