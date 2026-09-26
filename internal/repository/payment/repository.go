package payment

import (
	"context"
	"errors"
	"time"

	"github.com/insignificantGuy/fastPay/internal/models"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

var ErrDuplicateIdempotencyKey = errors.New("duplicate idempotency key")

// LedgerStamp is one reconciliation stamp: the provider's view of a payment,
// applied to fastPay's ledger row in a single batched transaction.
type LedgerStamp struct {
	PaymentID      string
	ProviderAmount int64
	ProviderStatus string
}

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(p *models.Payment) error {
	err := r.db.Create(p).Error
	if isUniqueViolation(err) {
		return ErrDuplicateIdempotencyKey
	}
	return err
}

func (r *Repository) Update(p *models.Payment) error {
	return r.db.Save(p).Error
}

func (r *Repository) Get(id string) (*models.Payment, error) {
	var p models.Payment
	if err := r.db.First(&p, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *Repository) GetByIdempotencyKey(key string) (*models.Payment, error) {
	var p models.Payment
	if err := r.db.First(&p, "idempotency_key = ?", key).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

func (r *Repository) CreateAttempt(a *models.PaymentAttempt) error {
	return r.db.Create(a).Error
}

func (r *Repository) ListAttempts(paymentID string) ([]models.PaymentAttempt, error) {
	var rows []models.PaymentAttempt
	err := r.db.Where("payment_id = ?", paymentID).Order("attempt_number").Find(&rows).Error
	return rows, err
}

func (r *Repository) UpsertLedger(e *models.LedgerEntry) error {
	var existing models.LedgerEntry
	err := r.db.Where("payment_id = ?", e.PaymentID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return r.db.Create(e).Error
	}
	if err != nil {
		return err
	}
	existing.Amount = e.Amount
	existing.Status = e.Status
	if e.ProviderReportedAmount != nil {
		existing.ProviderReportedAmount = e.ProviderReportedAmount
	}
	if e.ProviderReportedStatus != nil {
		existing.ProviderReportedStatus = e.ProviderReportedStatus
	}
	if e.ReconciledAt != nil {
		existing.ReconciledAt = e.ReconciledAt
	}
	return r.db.Save(&existing).Error
}

func (r *Repository) GetLedger(paymentID string) (*models.LedgerEntry, error) {
	var e models.LedgerEntry
	if err := r.db.First(&e, "payment_id = ?", paymentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

// ListLedger returns at most limit ledger rows, newest first.
// limit <= 0 means "return everything" (use only for small datasets).
func (r *Repository) ListLedger(ctx context.Context, limit int) ([]models.LedgerEntry, error) {
	var rows []models.LedgerEntry
	q := r.db.WithContext(ctx).Order("created_at DESC, id DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&rows).Error
	return rows, err
}

// ReconcileStamps stamps provider-reported amounts/statuses onto ledger rows
// and marks them reconciled, in a single transaction per call.
// A nil/empty slice is a no-op. Stamps with an empty PaymentID are ignored.
func (r *Repository) ReconcileStamps(ctx context.Context, stamps []LedgerStamp) error {
	if len(stamps) == 0 {
		return nil
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, s := range stamps {
			if s.PaymentID == "" {
				continue
			}
			var existing models.LedgerEntry
			if err := tx.First(&existing, "payment_id = ?", s.PaymentID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				return err
			}
			ps := s.ProviderStatus
			pa := s.ProviderAmount
			existing.ProviderReportedStatus = &ps
			existing.ProviderReportedAmount = &pa
			existing.ReconciledAt = &now
			if err := tx.Save(&existing).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}
