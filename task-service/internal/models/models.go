package models

import (
	"gorm.io/gorm"
	"time"
)

type User struct {
	ID        uint           `json:"id" gorm:"primaryKey"`
	Name      string         `json:"name" gorm:"not null"`
	Email     string         `json:"email" gorm:"uniqueIndex;not null"`
	Password  string         `json:"-" gorm:"not null"`
	Role      string         `json:"role" gorm:"default:user"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `json:"deleted_at" gorm:"index"`
}

type Project struct {
	ID          uint           `json:"id" gorm:"primaryKey"`
	Name        string         `json:"name" gorm:"not null"`
	Description string         `json:"description"`
	OwnerID     uint           `json:"owner_id" gorm:"not null"`
	Owner       *User          `json:"owner" gorm:"foreignKey:OwnerID"`
	Tasks       []Task         `json:"tasks,omitempty" gorm:"foreignKey:ProjectID"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"deleted_at" gorm:"index"`
}

type Task struct {
	ID          uint           `json:"id" gorm:"primaryKey"`
	Title       string         `json:"title" gorm:"not null"`
	Description string         `json:"description"`
	ProjectID   uint           `json:"project_id" gorm:"not null"`
	Project     Project        `json:"project" gorm:"foreignKey:ProjectID"`
	AssigneeID  *uint          `json:"assignee_id"`
	Assignee    *User          `json:"assignee,omitempty" gorm:"foreignKey:AssigneeID"`
	CreatorID   *uint          `json:"creator_id" gorm:"not null"`
	Creator     *User          `json:"creator,omitempty" gorm:"foreignKey:CreatorID"`
	Status      string         `json:"status" gorm:"not null;default:'Not Started'"`
	Priority    string         `json:"priority"`
	Estimate    string         `json:"estimate"`
	DueDate     *time.Time     `json:"due_date"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"deleted_at" gorm:"index"`
}
