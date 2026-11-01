package config

import "math"

// CreditBonus is a separate credit grant; it does not change the main quota.
type CreditBonus struct {
	DisplayName  string  `json:"displayName"`
	CurrentUsage float64 `json:"currentUsage"`
	UsageLimit   float64 `json:"usageLimit"`
	ExpiresAt    int64   `json:"expiresAt"`
	Status       string  `json:"status"`
}

func (b CreditBonus) IsActive(now int64) bool {
	return b.Status == "ACTIVE" && (b.ExpiresAt == 0 || b.ExpiresAt > now)
}

// QuotaTotals is shared by the dashboard and routing so both count valid grants.
func (a Account) QuotaTotals(now int64) (total, remaining float64) {
	add := func(limit, used float64) {
		if limit > 0 {
			total += limit
			remaining += math.Max(0, limit-math.Max(0, used))
		}
	}
	add(a.UsageLimit, a.UsageCurrent)
	if a.TrialStatus == "ACTIVE" && (a.TrialExpiresAt == 0 || a.TrialExpiresAt > now) {
		add(a.TrialUsageLimit, a.TrialUsageCurrent)
	}
	for _, bonus := range a.Bonuses {
		if bonus.IsActive(now) {
			add(bonus.UsageLimit, bonus.CurrentUsage)
		}
	}
	return
}
