package codex

// The `account/rateLimits/read` result, modeled field by field as observed
// on Codex CLI 0.150.1 (docs/S6_CODEX.md). Every field is a pointer: the
// difference between "absent" and "zero" must survive decoding, because a
// missing reset time or duration is rendered as "no countdown" and never as
// a fake zero. Only the fields S6 presents are modeled — no account email,
// no account ID, no opaque reset-credit identifiers, and no raw payload
// ever leaves this file.
type rateLimitsReadResult struct {
	RateLimits     *rateLimitSnapshot            `json:"rateLimits"`
	RateLimitsByID map[string]*rateLimitSnapshot `json:"rateLimitsByLimitId"`
	ResetCredits   *resetCreditsSummary          `json:"rateLimitResetCredits"`
}

// rateLimitSnapshot is one limit family. The primary/secondary windows are
// treated independently so a future CLI may add, drop, or reorder them.
type rateLimitSnapshot struct {
	LimitID              string  `json:"limitId"`
	Primary              *window `json:"primary"`
	Secondary            *window `json:"secondary"`
	PlanType             *string `json:"planType"`
	RateLimitReachedType *string `json:"rateLimitReachedType"`
}

// window is one rate-limit window. usedPercent is the only required field;
// a window without a sane usage is not usable.
type window struct {
	UsedPercent        *float64 `json:"usedPercent"`
	WindowDurationMins *int64   `json:"windowDurationMins"`
	ResetsAt           *int64   `json:"resetsAt"`
}

// resetCreditsSummary is the optional reset-credit block. Only the count is
// modeled: the per-credit identifiers and statuses are deliberately not.
type resetCreditsSummary struct {
	AvailableCount *int64 `json:"availableCount"`
}
