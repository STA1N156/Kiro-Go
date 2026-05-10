package proxy

import "testing"

func TestNormalizedUsageLimitKeepsPlanLimitWithoutOverage(t *testing.T) {
	breakdown := UsageBreakdown{
		CurrentUsage: 1000,
		UsageLimit:   1000,
	}

	if got := normalizedUsageLimit(breakdown, false); got != 1000 {
		t.Fatalf("expected normal account limit to stay 1000, got %.0f", got)
	}
}

func TestNormalizedUsageLimitIgnoresKiroOveragePricingWithoutManualSwitch(t *testing.T) {
	breakdown := UsageBreakdown{
		CurrentUsage: 1000,
		UsageLimit:   10000,
		OverageRate:  0.04,
	}

	if got := normalizedUsageLimit(breakdown, false); got != 1000 {
		t.Fatalf("expected account to stay at plan limit 1000 without manual overage switch, got %.0f", got)
	}
}

func TestNormalizedUsageLimitRaisesLimitWhenManualSwitchIsOn(t *testing.T) {
	breakdown := UsageBreakdown{
		CurrentUsage: 1000,
		UsageLimit:   1000,
	}

	if got := normalizedUsageLimit(breakdown, true); got != overageEnabledUsageLimit {
		t.Fatalf("expected manual overage switch to raise limit to %.0f, got %.0f", overageEnabledUsageLimit, got)
	}
}

func TestNormalizedUsageLimitDoesNotLowerHigherLimit(t *testing.T) {
	breakdown := UsageBreakdown{
		CurrentUsage: 1000,
		UsageLimit:   20000,
	}

	if got := normalizedUsageLimit(breakdown, true); got != 20000 {
		t.Fatalf("expected higher server limit to stay 20000, got %.0f", got)
	}
}
