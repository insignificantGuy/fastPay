package reconciliation

import "github.com/insignificantGuy/fastPay/internal/providers"

type LedgerRow struct {
	PaymentID string
	Amount    int64
	Status    string
}

type Item struct {
	PaymentID      string `json:"payment_id"`
	LedgerAmount   int64  `json:"ledger_amount,omitempty"`
	LedgerStatus   string `json:"ledger_status,omitempty"`
	ProviderAmount int64  `json:"provider_amount,omitempty"`
	ProviderStatus string `json:"provider_status,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

type Report struct {
	Matches         []Item `json:"matches"`
	Mismatches      []Item `json:"mismatches"`
	OrphansLedger   []Item `json:"orphans_ledger"`
	OrphansProvider []Item `json:"orphans_provider"`
}

// Compare ledger rows against provider-processed charges by payment ID.
func Compare(ledger []LedgerRow, processed []providers.ProcessedCharge) Report {
	byPay := map[string]providers.ProcessedCharge{}
	for _, p := range processed {
		if p.PaymentID == "" {
			continue
		}
		byPay[p.PaymentID] = p
	}
	seen := map[string]bool{}
	rep := Report{}
	for _, row := range ledger {
		seen[row.PaymentID] = true
		p, ok := byPay[row.PaymentID]
		if !ok {
			rep.OrphansLedger = append(rep.OrphansLedger, Item{
				PaymentID: row.PaymentID, LedgerAmount: row.Amount, LedgerStatus: row.Status,
				Reason: "present on ledger only",
			})
			continue
		}
		item := Item{
			PaymentID: row.PaymentID, LedgerAmount: row.Amount, LedgerStatus: row.Status,
			ProviderAmount: p.Amount, ProviderStatus: p.Status,
		}
		if row.Amount != p.Amount || row.Status != p.Status {
			item.Reason = "amount or status drift"
			rep.Mismatches = append(rep.Mismatches, item)
			continue
		}
		rep.Matches = append(rep.Matches, item)
	}
	for id, p := range byPay {
		if seen[id] {
			continue
		}
		rep.OrphansProvider = append(rep.OrphansProvider, Item{
			PaymentID: p.PaymentID, ProviderAmount: p.Amount, ProviderStatus: p.Status,
			Reason: "present on provider only",
		})
	}
	return rep
}
