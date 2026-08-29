package hindsight

import (
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// rateNoiseEpsilon bounds float noise in derived percentages. A value
// beyond it outside [0,100] is materially impossible and dropped rather
// than clamped.
const rateNoiseEpsilon = 1e-9

// counterDelta returns (current-previous, true) when both samples exist,
// are finite, and the counter did not reset (current >= previous). A reset
// or a missing side yields no delta — the caller shows "unavailable", and
// the next interval derives against the new baseline.
func counterDelta(prev, curr *float64) (float64, bool) {
	if prev == nil || curr == nil {
		return 0, false
	}
	if !isFinite(*prev) || !isFinite(*curr) {
		return 0, false
	}
	if *curr < *prev {
		return 0, false
	}

	return *curr - *prev, true
}

// perMinute converts a delta over an elapsed interval into a per-minute
// rate. A non-positive elapsed time yields nothing (the caller shows
// "unavailable" rather than inventing a rate).
func perMinute(delta float64, elapsed time.Duration) *float64 {
	if elapsed <= 0 {
		return nil
	}
	rate := delta / elapsed.Seconds() * 60

	return &rate
}

// percentOf returns part/all*100 when meaningful. A non-positive total is
// unavailable rather than zero; tiny negative noise clamps to 0 and tiny
// overshoot above 100 clamps to 100, but a materially impossible value is
// dropped.
func percentOf(part, all float64) *float64 {
	if all <= 0 {
		return nil
	}
	if part < 0 {
		if part >= -rateNoiseEpsilon {
			part = 0
		} else {
			return nil
		}
	}
	pct := part / all * 100
	switch {
	case pct > 100:
		if pct <= 100+rateNoiseEpsilon {
			pct = 100
		} else {
			return nil
		}
	case pct < 0:
		return nil
	}

	return &pct
}

// deriveActivity derives one snapshot's activity rates from the previous
// committed counter baseline. A nil previous (first successful sample) or
// a non-positive elapsed interval leaves every rate unavailable; each
// field degrades independently, so one reset family never blanks the rest.
func deriveActivity(prev, curr *metricsCounters, elapsed time.Duration) domain.HindsightActivity {
	if prev == nil {
		return domain.HindsightActivity{}
	}

	return domain.HindsightActivity{
		RetainPerMin:       ratePerMin(prev.RetainTotal, curr.RetainTotal, elapsed),
		RecallPerMin:       ratePerMin(prev.RecallTotal, curr.RecallTotal, elapsed),
		ReflectPerMin:      ratePerMin(prev.ReflectTotal, curr.ReflectTotal, elapsed),
		ConsolidatePerMin:  ratePerMin(prev.ConsolidateTotal, curr.ConsolidateTotal, elapsed),
		LLMCallsPerMin:     ratePerMin(prev.LLMCallsTotal, curr.LLMCallsTotal, elapsed),
		InputTokensPerMin:  ratePerMin(prev.InputTokensTotal, curr.InputTokensTotal, elapsed),
		OutputTokensPerMin: ratePerMin(prev.OutputTokensTotal, curr.OutputTokensTotal, elapsed),
		HTTP5xxPercent:     http5xxPercent(prev, curr),
	}
}

// ratePerMin derives one per-minute counter rate, or nil when the pair is
// not derivable (missing side, reset, or bad elapsed).
func ratePerMin(prev, curr *float64, elapsed time.Duration) *float64 {
	delta, ok := counterDelta(prev, curr)
	if !ok {
		return nil
	}

	return perMinute(delta, elapsed)
}

// http5xxPercent derives the interval's 5xx share of all HTTP requests. A
// zero request delta is unavailable rather than 0%: no traffic carries no
// error-rate information.
func http5xxPercent(prev, curr *metricsCounters) *float64 {
	deltaAll, ok := counterDelta(prev.HTTPRequestsTotal, curr.HTTPRequestsTotal)
	if !ok {
		return nil
	}
	delta5xx, ok := counterDelta(prev.HTTP5xxTotal, curr.HTTP5xxTotal)
	if !ok {
		return nil
	}

	return percentOf(delta5xx, deltaAll)
}

// domainRuntime maps the scrape gauges onto the domain runtime block.
// DBPoolWaiting passes through as the optional fallback for the /health
// value of the same name; DBPoolMin stays adapter-side (not displayed).
func domainRuntime(g metricsGauges) domain.HindsightRuntime {
	return domain.HindsightRuntime{
		DBPoolSize:     g.DBPoolSize,
		DBPoolIdle:     g.DBPoolIdle,
		DBPoolMax:      g.DBPoolMax,
		DBPoolWaiting:  g.DBPoolWaiting,
		HTTPInProgress: g.HTTPInProgress,
	}
}

// domainQueue maps the optional backlog gauges onto the domain queue
// block; nil stays nil so the UI can omit the whole section.
func domainQueue(q *queueGauges) *domain.HindsightQueue {
	if q == nil {
		return nil
	}

	return &domain.HindsightQueue{
		Pending:              q.Pending,
		Processing:           q.Processing,
		Failed:               q.Failed,
		ConsolidationBacklog: q.ConsolidationBacklog,
		ConsolidationFailed:  q.ConsolidationFailed,
	}
}
