package routing_test

import (
	"testing"

	"github.com/insignificantGuy/fastPay/internal/providers/mock"
	"github.com/insignificantGuy/fastPay/internal/routing"
)

func TestWeightedRoundRobinFollowsConfiguredWeights(t *testing.T) {
	a := mock.New(mock.Config{ID: "a", Available: true, Mode: mock.ModeSuccess})
	b := mock.New(mock.Config{ID: "b", Available: true, Mode: mock.ModeSuccess})
	engine := routing.NewEngine(routing.Config{
		Providers: []routing.WeightedProvider{
			{Provider: a, Weight: 2},
			{Provider: b, Weight: 1},
		},
	})

	pay := routing.Payment{Amount: 100, Currency: "USD"}
	got := map[string]int{}
	const n = 9
	for i := 0; i < n; i++ {
		p, err := engine.SelectProvider(pay)
		if err != nil {
			t.Fatal(err)
		}
		got[p.ID()]++
	}
	if got["a"] != 6 || got["b"] != 3 {
		t.Fatalf("got counts %v, want a=6 b=3 over %d calls", got, n)
	}
}

func TestSelectProviderUsesRealMockInstances(t *testing.T) {
	a := mock.New(mock.Config{ID: "provider-a", Available: true, Mode: mock.ModeSuccess})
	engine := routing.NewEngine(routing.Config{
		Providers: []routing.WeightedProvider{{Provider: a, Weight: 1}},
	})
	p, err := engine.SelectProvider(routing.Payment{})
	if err != nil {
		t.Fatal(err)
	}
	if p != a {
		t.Fatal("engine must return the same Provider instance, not a stub")
	}
}

func TestUnavailableProviderIsSkipped(t *testing.T) {
	a := mock.New(mock.Config{ID: "a", Available: false, Mode: mock.ModeSuccess})
	b := mock.New(mock.Config{ID: "b", Available: true, Mode: mock.ModeSuccess})
	engine := routing.NewEngine(routing.Config{
		Providers: []routing.WeightedProvider{
			{Provider: a, Weight: 5},
			{Provider: b, Weight: 1},
		},
	})
	for i := 0; i < 5; i++ {
		p, err := engine.SelectProvider(routing.Payment{})
		if err != nil {
			t.Fatal(err)
		}
		if p.ID() != "b" {
			t.Fatalf("selected %s, want b", p.ID())
		}
	}
}

func TestAllUnavailableReturnsClearError(t *testing.T) {
	a := mock.New(mock.Config{ID: "a", Available: false, Mode: mock.ModeSuccess})
	engine := routing.NewEngine(routing.Config{
		Providers: []routing.WeightedProvider{{Provider: a, Weight: 1}},
	})
	_, err := engine.SelectProvider(routing.Payment{})
	if err == nil {
		t.Fatal("expected error when no provider is available")
	}
}

func TestEmptyConfigReturnsError(t *testing.T) {
	engine := routing.NewEngine(routing.Config{})
	_, err := engine.SelectProvider(routing.Payment{})
	if err == nil {
		t.Fatal("expected error")
	}
}
