package reconciliation

import (
	"testing"

	"github.com/insignificantGuy/fastPay/internal/providers"
)

func TestCompareMatchesMismatchesAndOrphans(t *testing.T) {
	ledger := []LedgerRow{
		{PaymentID: "p1", Amount: 1000, Status: "succeeded"},
		{PaymentID: "p2", Amount: 500, Status: "succeeded"},
		{PaymentID: "p3", Amount: 200, Status: "failed"},
	}
	processed := []providers.ProcessedCharge{
		{PaymentID: "p1", Amount: 1000, Status: "succeeded"},
		{PaymentID: "p2", Amount: 999, Status: "succeeded"},
		{PaymentID: "p4", Amount: 50, Status: "succeeded"},
	}
	rep := Compare(ledger, processed)
	if len(rep.Matches) != 1 || rep.Matches[0].PaymentID != "p1" {
		t.Fatalf("matches=%+v", rep.Matches)
	}
	if len(rep.Mismatches) != 1 || rep.Mismatches[0].PaymentID != "p2" {
		t.Fatalf("mismatches=%+v", rep.Mismatches)
	}
	if len(rep.OrphansLedger) != 1 || rep.OrphansLedger[0].PaymentID != "p3" {
		t.Fatalf("orphans ledger=%+v", rep.OrphansLedger)
	}
	if len(rep.OrphansProvider) != 1 || rep.OrphansProvider[0].PaymentID != "p4" {
		t.Fatalf("orphans provider=%+v", rep.OrphansProvider)
	}
}
