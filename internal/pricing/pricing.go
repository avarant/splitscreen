// Package pricing turns raw token counters into dollars.
//
// The gateway prices work, not the runner: a runner does not know the price
// table, the billing mode, or which key paid. Tables are versioned so a
// corrected rate can be applied to history by recomputation rather than lost.
package pricing

import (
	"fmt"
	"strings"
)

// Rates are dollars per million tokens for one model.
type Rates struct {
	Input  float64
	Output float64
	// CacheWriteMult and CacheReadMult are multipliers on the input rate.
	// Cache writes cost a premium and reads a fraction; collapsing them into
	// the input rate is the single easiest way to be wrong by an order of
	// magnitude on long threads.
	CacheWriteMult float64
	CacheReadMult  float64
	// CacheWriteMult1h applies when a turn used the longer cache TTL.
	CacheWriteMult1h float64
}

// Table is a versioned set of model rates.
type Table struct {
	Version string
	Rates   map[string]Rates
	// Default is used for models absent from Rates. Zero value means "refuse to
	// guess", which surfaces as an unpriced turn rather than a wrong number.
	Default *Rates
}

// Counters are the four token totals for a turn.
type Counters struct {
	Input      int64
	CacheWrite int64
	CacheRead  int64
	Output     int64
	// TTLHint is "1h" when the longer cache tier applied.
	TTLHint string
}

// Cost prices a turn. The second return is false when the model is unknown and
// the table has no default: an unpriced turn must stay visibly unpriced.
func (t *Table) Cost(model string, c Counters) (float64, bool) {
	r, ok := t.lookup(model)
	if !ok {
		return 0, false
	}
	writeMult := r.CacheWriteMult
	if c.TTLHint == "1h" && r.CacheWriteMult1h > 0 {
		writeMult = r.CacheWriteMult1h
	}
	const perMillion = 1_000_000.0
	total := float64(c.Input)/perMillion*r.Input +
		float64(c.CacheWrite)/perMillion*r.Input*writeMult +
		float64(c.CacheRead)/perMillion*r.Input*r.CacheReadMult +
		float64(c.Output)/perMillion*r.Output
	return total, true
}

func (t *Table) lookup(model string) (Rates, bool) {
	if r, ok := t.Rates[model]; ok {
		return r, true
	}
	// Model ids carry suffixes (dates, regions, provider prefixes). Fall back to
	// the longest configured prefix match before giving up.
	best := ""
	for k := range t.Rates {
		if strings.Contains(model, k) && len(k) > len(best) {
			best = k
		}
	}
	if best != "" {
		return t.Rates[best], true
	}
	if t.Default != nil {
		return *t.Default, true
	}
	return Rates{}, false
}

// Validate rejects a table that would silently produce nonsense.
func (t *Table) Validate() error {
	if t.Version == "" {
		return fmt.Errorf("pricing: table version is required — costs are recorded against it")
	}
	if len(t.Rates) == 0 && t.Default == nil {
		return fmt.Errorf("pricing: table %q has no rates and no default", t.Version)
	}
	for name, r := range t.Rates {
		if r.Input < 0 || r.Output < 0 || r.CacheReadMult < 0 || r.CacheWriteMult < 0 {
			return fmt.Errorf("pricing: model %q has a negative rate", name)
		}
	}
	return nil
}
