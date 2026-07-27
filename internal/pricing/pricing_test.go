package pricing

import (
	"math"
	"testing"
)

func table() *Table {
	return &Table{
		Version: "test-1",
		Rates: map[string]Rates{
			"claude-opus": {
				Input: 15, Output: 75,
				CacheWriteMult: 1.25, CacheReadMult: 0.1, CacheWriteMult1h: 2.0,
			},
		},
	}
}

func TestCacheMultipliersApplied(t *testing.T) {
	tb := table()

	// 1M cache reads at 0.1x of a $15 input rate is $1.50 — an order of
	// magnitude less than pricing them as plain input, which is exactly the
	// error keeping the counters separate exists to prevent.
	got, ok := tb.Cost("claude-opus", Counters{CacheRead: 1_000_000})
	if !ok {
		t.Fatal("model should be priced")
	}
	if math.Abs(got-1.5) > 1e-9 {
		t.Fatalf("cache read cost = %v, want 1.50", got)
	}

	// Cache writes cost a premium, and more at the longer TTL.
	short, _ := tb.Cost("claude-opus", Counters{CacheWrite: 1_000_000})
	long, _ := tb.Cost("claude-opus", Counters{CacheWrite: 1_000_000, TTLHint: "1h"})
	if math.Abs(short-18.75) > 1e-9 {
		t.Fatalf("5m cache write = %v, want 18.75", short)
	}
	if math.Abs(long-30) > 1e-9 {
		t.Fatalf("1h cache write = %v, want 30", long)
	}
	if long <= short {
		t.Error("the longer cache tier should cost more, not less")
	}
}

func TestModelPrefixMatch(t *testing.T) {
	tb := table()
	// Real model ids carry dates and provider prefixes.
	if _, ok := tb.Cost("us.anthropic.claude-opus-5-20260101", Counters{Input: 1}); !ok {
		t.Fatal("a suffixed model id should match its configured prefix")
	}
}

// An unknown model must stay visibly unpriced rather than being guessed at.
func TestUnknownModelIsNotPricedAsZero(t *testing.T) {
	tb := table()
	if _, ok := tb.Cost("some-other-model", Counters{Input: 1_000_000}); ok {
		t.Fatal("an unknown model was priced; it must be reported as unpriced")
	}

	tb.Default = &Rates{Input: 1, Output: 1}
	got, ok := tb.Cost("some-other-model", Counters{Input: 1_000_000})
	if !ok || math.Abs(got-1) > 1e-9 {
		t.Fatalf("with a default: got %v ok=%v, want 1", got, ok)
	}
}

func TestValidate(t *testing.T) {
	if err := (&Table{Rates: map[string]Rates{"m": {}}}).Validate(); err == nil {
		t.Error("a table without a version was accepted; costs are recorded against it")
	}
	if err := (&Table{Version: "v"}).Validate(); err == nil {
		t.Error("a table with no rates and no default was accepted")
	}
	bad := &Table{Version: "v", Rates: map[string]Rates{"m": {Input: -1}}}
	if err := bad.Validate(); err == nil {
		t.Error("a negative rate was accepted")
	}
	if err := table().Validate(); err != nil {
		t.Errorf("a good table was rejected: %v", err)
	}
}
