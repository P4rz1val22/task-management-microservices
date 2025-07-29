package handlers

import (
	"github.com/P4rz1val22/task-management-api/internal/database"
	"github.com/P4rz1val22/task-management-api/internal/models"
	"github.com/gin-gonic/gin"
	"net/http"
)

type UpdateUserRequest struct {
	Name  string `json:"name" binding:"required"`
	Email string `json:"email" binding:"required,email"`
}

// @Summary		Get current user profile
// @Description	Get the profile information of the currently authenticated user
// @Tags			users
// @Produce		json
// @Security		BearerAuth
// @Success		200	{object}	map[string]interface{}
// @Failure		401	{object}	map[string]interface{}
// @Failure		404	{object}	map[string]interface{}
// @Router			/users/me [get]
func GetCurrentUser(c *gin.Context) {
	userID := c.GetUint("user_id")
	var user models.User
	if err := database.DB.First(&user, userID).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid user ID"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"user": user,
	})
}

// @Summary		Update current user profile
// @Description	Update the profile information of the currently authenticated user
// @Tags			users
// @Accept			json
// @Produce		json
// @Security		BearerAuth
// @Param			user	body		UpdateUserRequest	true	"User update data"
// @Success		200		{object}	map[string]interface{}
// @Failure		400		{object}	map[string]interface{}
// @Failure		401		{object}	map[string]interface{}
// @Failure		404		{object}	map[string]interface{}
// @Router			/users/me [put]
func UpdateCurrentUser(c *gin.Context) {
	userID := c.GetUint("user_id")
	var req UpdateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Duplicate email
	var existingUser models.User
	if err := database.DB.Where("email = ? AND id != ?", req.Email, userID).First(&existingUser).Error; err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Email already taken"})
		return
	}

	// User not found
	var user models.User
	if err := database.DB.First(&user, userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	user.Name = req.Name
	user.Email = req.Email

	// Update user
	if err := database.DB.Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update user"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "User updated successfully",
		"user":    user,
	})
}
