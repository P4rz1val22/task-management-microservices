package models

import (
	"gorm.io/gorm"
	"time"
)

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
