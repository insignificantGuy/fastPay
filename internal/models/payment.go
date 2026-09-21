package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	PaymentStatusPending             = "pending"
	PaymentStatusSucceeded           = "succeeded"
	PaymentStatusFailed              = "failed"
	PaymentStatusFailedPendingReview = "failed_pending_review"
)

type Payment struct {
	ID             string `gorm:"type:uuid;primaryKey"`
	IdempotencyKey string `gorm:"type:varchar(128);uniqueIndex;not null"`
	Amount         int64  `gorm:"not null"`
	Currency       string `gorm:"type:varchar(3);not null"`
	Status         string `gorm:"type:varchar(32);not null"`
	ProviderID     string `gorm:"type:varchar(64);not null;default:''"`
	LastOutcome    string `gorm:"type:varchar(32);not null;default:''"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (p *Payment) BeforeCreate(tx *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	return nil
}

type PaymentAttempt struct {
	ID            string `gorm:"type:uuid;primaryKey"`
	PaymentID     string `gorm:"type:uuid;index;not null"`
	AttemptNumber int    `gorm:"not null"`
	Outcome       string `gorm:"type:varchar(32);not null"`
	Retryable     bool
	ProviderTxnID string `gorm:"type:varchar(64);not null;default:''"`
	CreatedAt     time.Time
}

func (a *PaymentAttempt) BeforeCreate(tx *gorm.DB) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	return nil
}

type LedgerEntry struct {
	ID                     string `gorm:"type:uuid;primaryKey"`
	PaymentID              string `gorm:"type:uuid;uniqueIndex;not null"`
	Amount                 int64  `gorm:"not null"`
	Status                 string `gorm:"type:varchar(32);not null"`
	ProviderReportedAmount *int64
	ProviderReportedStatus *string `gorm:"type:varchar(32)"`
	ReconciledAt           *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

func (e *LedgerEntry) BeforeCreate(tx *gorm.DB) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	return nil
}
