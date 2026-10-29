package entity

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type User struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;not null" json:"id"`
	Name      string         `gorm:"size:100;not null"              json:"name"`
	Email     string         `gorm:"size:255;uniqueIndex;not null"  json:"email"`
	Password  string         `gorm:"size:255;not null"              json:"-"`
	CreatedAt time.Time      `                                      json:"created_at"`
	UpdatedAt time.Time      `                                      json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index"                          json:"-"`
}

func (u *User) BeforeCreate(_ *gorm.DB) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	return nil
}

func (User) TableName() string { return "users" }
