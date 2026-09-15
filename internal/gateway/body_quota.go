package gateway

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// Some providers state remaining budget in the response body rather than in
// rate-limit headers, which the header observer cannot see. It is a balance,
// not a window, so it is stored with no limit: inventing a ceiling would turn
// a healthy balance into a fake percentage.
type bodyQuotaField struct {
	field string
	unit  string
}

var bodyQuotaFields = map[string]bodyQuotaField{
	"hyper": {field: "hypercredits", unit: "credits"},
}

// ObserveBodyQuota reports whether anything was stored, so a caller can tell
// an absent signal from a zero balance.
func (l *Ledger) ObserveBodyQuota(platform string, keyID int64, body []byte) bool {
	spec, ok := bodyQuotaFields[platform]
	if !ok || len(body) == 0 {
		return false
	}

	var parsed struct {
		Usage struct {
			Remaining map[string]json.Number `json:"remaining"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return false
	}
	raw, ok := parsed.Usage.Remaining[spec.field]
	if !ok {
		return false
	}
	balance, err := strconv.ParseFloat(raw.String(), 64)
	if err != nil || balance < 0 {
		return false
	}

	obs := &QuotaObservation{
		Platform:   platform,
		PoolKey:    PoolKey(platform, keyID),
		ObservedAt: l.clock().Unix(),
		Tokens: MetricObservation{
			// Rounded rather than truncated: a fractional balance of 0.7
			// credits is not zero remaining.
			Remaining: int64(math.Round(balance)), HasRemaining: true,
		},
		// The provider's own digits, not a re-rendered float: parsing and
		// reprinting 55.639165814 yields 55.639165813999995.
		Raw: fmt.Sprintf("%s %s remaining", raw.String(), spec.unit),
	}
	l.persistObservation(obs)
	return true
}
