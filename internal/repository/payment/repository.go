package payment

import (
	"github.com/insignificantGuy/fastPay/internal/models"
	"gorm.io/gorm"
)

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(p *models.Payment) error {
	return r.db.Create(p).Error
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
