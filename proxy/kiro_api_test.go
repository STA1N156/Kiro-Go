package proxy

import "testing"

func TestNormalizedUsageLimitKeepsPlanLimitWithoutOverage(t *testing.T) {
	breakdown := UsageBreakdown{
		CurrentUsage: 1000,
		UsageLimit:   1000,
	}

	if got := normalizedUsageLimit(breakdown); got != 1000 {
		t.Fatalf("expected normal account limit to stay 1000, got %.0f", got)
	}
}

func TestNormalizedUsageLimitRaisesLimitWhenOverageRateExists(t *testing.T) {
	breakdown := UsageBreakdown{
		CurrentUsage: 1000,
		UsageLimit:   1000,
		OverageRate:  0.04,
	}

	if got := normalizedUsageLimit(breakdown); got != overageEnabledUsageLimit {
		t.Fatalf("expected overage-enabled limit %.0f, got %.0f", overageEnabledUsageLimit, got)
	}
}

func TestNormalizedUsageLimitRaisesLimitWhenOverageFlagExists(t *testing.T) {
	breakdown := UsageBreakdown{
		CurrentUsage:     1000,
		UsageLimit:       1000,
		OverageEnabled:   true,
		IsOverageEnabled: true,
	}

	if got := normalizedUsageLimit(breakdown); got != overageEnabledUsageLimit {
		t.Fatalf("expected overage-enabled flag to raise limit to %.0f, got %.0f", overageEnabledUsageLimit, got)
	}
}

func TestNormalizedUsageLimitDoesNotLowerHigherLimit(t *testing.T) {
	breakdown := UsageBreakdown{
		CurrentUsage:    1000,
		UsageLimit:      20000,
		OverageEnabled:  true,
		OverageStatus:   "ENABLED",
	}

	if got := normalizedUsageLimit(breakdown); got != 20000 {
		t.Fatalf("expected higher server limit to stay 20000, got %.0f", got)
	}
}
