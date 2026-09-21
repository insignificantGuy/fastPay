package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	PaymentStatusPending   = "pending"
	PaymentStatusSucceeded = "succeeded"
	PaymentStatusFailed    = "failed"
)

type Payment struct {
	ID         string `gorm:"type:uuid;primaryKey"`
	Amount     int64  `gorm:"not null"`
	Currency   string `gorm:"type:varchar(3);not null"`
	Status     string `gorm:"type:varchar(32);not null"`
	ProviderID string `gorm:"type:varchar(64)"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (p *Payment) BeforeCreate(tx *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	return nil
}
