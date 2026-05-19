package auth

import "strings"

const (
	PlanCodeNormal   = "normal"
	PlanCodePremium  = "premium"
	PlanCodeBasic    = "basic"
	PlanCodeUnlimited = "unlimited"
)

// NormalizePlanCode converts legacy aliases to the current canonical codes.
func NormalizePlanCode(planCode string) string {
	switch strings.ToLower(strings.TrimSpace(planCode)) {
	case PlanCodeBasic:
		return PlanCodeNormal
	case PlanCodeUnlimited:
		return PlanCodePremium
	default:
		return strings.ToLower(strings.TrimSpace(planCode))
	}
}

// IsPremiumPlan reports whether the provided plan grants unlimited consultations.
func IsPremiumPlan(planCode string) bool {
	return NormalizePlanCode(planCode) == PlanCodePremium
}
