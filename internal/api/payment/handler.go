package payment

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/insignificantGuy/fastPay/internal/models"
	"github.com/insignificantGuy/fastPay/internal/providers"
	"github.com/insignificantGuy/fastPay/internal/routing"
)

type Store interface {
	Create(p *models.Payment) error
	Update(p *models.Payment) error
}

type Router interface {
	SelectProvider(payment routing.Payment) (providers.Provider, error)
}

type Service struct {
	store  Store
	router Router
}

func NewService(store Store, router Router) *Service {
	return &Service{store: store, router: router}
}

type createRequest struct {
	Amount   int64  `json:"amount" binding:"required,gt=0"`
	Currency string `json:"currency" binding:"required,len=3"`
}

type createResponse struct {
	ID         string `json:"id"`
	Amount     int64  `json:"amount"`
	Currency   string `json:"currency"`
	Status     string `json:"status"`
	ProviderID string `json:"provider_id"`
	Message    string `json:"message,omitempty"`
}

func (s *Service) Create(c *gin.Context) {
	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	p := &models.Payment{
		ID:       uuid.NewString(),
		Amount:   req.Amount,
		Currency: req.Currency,
		Status:   models.PaymentStatusPending,
	}
	if err := s.store.Create(p); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist payment"})
		return
	}

	provider, err := s.router.SelectProvider(routing.Payment{
		Amount:   req.Amount,
		Currency: req.Currency,
	})
	if err != nil {
		p.Status = models.PaymentStatusFailed
		_ = s.store.Update(p)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error(), "id": p.ID, "status": p.Status})
		return
	}

	p.ProviderID = provider.ID()
	result, err := provider.Charge(c.Request.Context(), providers.ChargeRequest{
		PaymentID: p.ID,
		Amount:    req.Amount,
		Currency:  req.Currency,
	})
	if err != nil {
		p.Status = models.PaymentStatusFailed
		_ = s.store.Update(p)
		c.JSON(http.StatusGatewayTimeout, createResponse{
			ID: p.ID, Amount: p.Amount, Currency: p.Currency,
			Status: p.Status, ProviderID: p.ProviderID, Message: err.Error(),
		})
		return
	}

	httpStatus, status, msg := mapOutcome(result)
	p.Status = status
	if err := s.store.Update(p); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update payment"})
		return
	}

	c.JSON(httpStatus, createResponse{
		ID:         p.ID,
		Amount:     p.Amount,
		Currency:   p.Currency,
		Status:     p.Status,
		ProviderID: p.ProviderID,
		Message:    msg,
	})
}

func mapOutcome(result providers.ChargeResult) (httpStatus int, status string, message string) {
	switch result.Outcome {
	case providers.OutcomeSuccess:
		return http.StatusCreated, models.PaymentStatusSucceeded, result.Message
	case providers.OutcomeDecline:
		return http.StatusUnprocessableEntity, models.PaymentStatusFailed, result.Message
	case providers.OutcomeTimeout:
		return http.StatusGatewayTimeout, models.PaymentStatusFailed, result.Message
	case providers.OutcomeAccepted:
		return http.StatusAccepted, models.PaymentStatusPending, result.Message
	default:
		return http.StatusInternalServerError, models.PaymentStatusFailed, fmt.Sprintf("unknown outcome %s", result.Outcome)
	}
}

func (s *Service) Register(rg *gin.RouterGroup) {
	rg.POST("/payments", s.Create)
}
