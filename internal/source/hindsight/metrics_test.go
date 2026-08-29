package hindsight

import "testing"

// mustSamples parses one valid exposition body or fails the test.
func mustSamples(t *testing.T, body string) []metricSample {
	t.Helper()
	samples, err := parseSelectedMetrics(body)
	if err != nil {
		t.Fatalf("parseSelectedMetrics: %v", err)
	}
	return samples
}

// mustCounters parses and aggregates one exposition body, returning the
// counters block for assertions.
func mustCounters(t *testing.T, body string) metricsCounters {
	t.Helper()
	counters, _, _ := aggregateMetrics(mustSamples(t, body))
	return counters
}

func TestParseSelectedMetricsSkipsNonSamples(t *testing.T) {
	body := `# HELP hindsight_llm_calls_total LLM calls.
# TYPE hindsight_llm_calls_total counter
hindsight_llm_calls_total{provider="p",bank="b-1"} 5 1724937600000
hindsight_llm_calls_total 6
# A plain comment
some_other_metric 1
hindsight_operation_total{operation="retain"} 2
hindsight_llm_calls_total{provider="p"} NaN
hindsight_llm_calls_total 7
`
	samples, err := parseSelectedMetrics(body)
	if err != nil {
		t.Fatalf("parseSelectedMetrics: %v", err)
	}
	if len(samples) != 4 {
		t.Fatalf("samples = %d (%+v), want 4", len(samples), samples)
	}
	for _, s := range samples {
		if !isFinite(s.value) {
			t.Errorf("non-finite sample parsed: %+v", s)
		}
	}
}

func TestParseSampleLineLabelEscapes(t *testing.T) {
	cases := []struct {
		name  string
		line  string
		value float64
		op    string
	}{
		{"escaped quote", `hindsight_operation_total{operation="re\"tain\""} 3`, 3, `re"tain"`},
		{"escaped backslash", `hindsight_operation_total{operation="re\\tain"} 4`, 4, `re\tain`},
		{"escaped newline", `hindsight_operation_total{operation="re\ntain"} 5`, 5, "re\ntain"},
	}
	for _, tc := range cases {
		samples, err := parseSelectedMetrics(tc.line)
		if err != nil {
			t.Errorf("%s: parse error %v", tc.name, err)
			continue
		}
		if len(samples) != 1 {
			t.Errorf("%s: samples = %d, want 1", tc.name, len(samples))
			continue
		}
		if samples[0].value != tc.value {
			t.Errorf("%s: value = %v, want %v", tc.name, samples[0].value, tc.value)
		}
		if got := samples[0].labels["operation"]; got != tc.op {
			t.Errorf("%s: operation = %q, want %q", tc.name, got, tc.op)
		}
	}
}

// Structurally malformed lines fail the whole parse — whatever family they
// claim — so a broken or truncated exposition cannot pass for a successful
// scrape (the baseline may only advance after /metrics parses).
func TestParseSelectedMetricsRejectsMalformedLines(t *testing.T) {
	bad := []string{
		`hindsight_llm_calls_total{unterminated="v} 1`,
		`hindsight_llm_calls_total{novalue} 1`,
		`hindsight_llm_calls_total 1 2 3`,
		`hindsight_llm_calls_total notanumber`,
		`hindsight_llm_calls_total`,
		// An error page or truncated response is not exposition either.
		`<html>502 Bad Gateway</html>`,
		`some_other_metric{broken`,
		`502`,
	}
	for _, line := range bad {
		got, err := parseSelectedMetrics(line)
		if err == nil {
			t.Errorf("parseSelectedMetrics(%q) = %+v, want an error", line, got)
		}
	}
}

// Non-finite values are valid exposition (Prometheus marks missing data
// with NaN) but unrepresentable for us: the sample is skipped as absence,
// never stored as zero and never an error.
func TestParseSelectedMetricsSkipsNonFiniteValues(t *testing.T) {
	body := `hindsight_llm_calls_total Inf
hindsight_llm_calls_total +Inf
hindsight_llm_calls_total -Inf
hindsight_db_pool_size NaN
hindsight_db_pool_size 7
`
	samples, err := parseSelectedMetrics(body)
	if err != nil {
		t.Fatalf("parseSelectedMetrics: %v", err)
	}
	if len(samples) != 1 || samples[0].value != 7 {
		t.Errorf("samples = %+v, want only the finite pool size 7", samples)
	}
}

func TestAggregateCountersSumAcrossLabels(t *testing.T) {
	body := `hindsight_operation_total{operation="retain",bank="b-1"} 2
hindsight_operation_total{operation="retain",bank="b-2"} 3
hindsight_operation_total{operation="unknown_op"} 99
hindsight_http_requests_total{status_class="2xx"} 10
hindsight_http_requests_total{status_class="5xx"} 4
hindsight_http_requests_total 20
`
	c := mustCounters(t, body)
	if c.RetainTotal == nil || *c.RetainTotal != 5 {
		t.Errorf("RetainTotal = %v, want 5 (unknown operations unclassified)", c.RetainTotal)
	}
	if c.HTTPRequestsTotal == nil || *c.HTTPRequestsTotal != 34 {
		t.Errorf("HTTPRequestsTotal = %v, want 34 (all series summed)", c.HTTPRequestsTotal)
	}
	if c.HTTP5xxTotal == nil || *c.HTTP5xxTotal != 4 {
		t.Errorf("HTTP5xxTotal = %v, want 4", c.HTTP5xxTotal)
	}
	if c.RecallTotal != nil {
		t.Errorf("RecallTotal = %v, want nil", c.RecallTotal)
	}
}

func TestAggregateGauges(t *testing.T) {
	body := `hindsight_db_pool_size 10
hindsight_db_pool_idle 4
hindsight_db_pool_min 1
hindsight_db_pool_max 12
hindsight_db_pool_waiting 2
hindsight_http_requests_in_progress 3
`
	_, g, q := aggregateMetrics(mustSamples(t, body))
	if g.DBPoolSize == nil || *g.DBPoolSize != 10 {
		t.Errorf("DBPoolSize = %v, want 10", g.DBPoolSize)
	}
	if g.DBPoolIdle == nil || *g.DBPoolIdle != 4 {
		t.Errorf("DBPoolIdle = %v, want 4", g.DBPoolIdle)
	}
	if g.DBPoolMax == nil || *g.DBPoolMax != 12 {
		t.Errorf("DBPoolMax = %v, want 12", g.DBPoolMax)
	}
	if g.DBPoolWaiting == nil || *g.DBPoolWaiting != 2 {
		t.Errorf("DBPoolWaiting = %v, want 2", g.DBPoolWaiting)
	}
	if g.HTTPInProgress == nil || *g.HTTPInProgress != 3 {
		t.Errorf("HTTPInProgress = %v, want 3", g.HTTPInProgress)
	}
	if q != nil {
		t.Errorf("Queue = %+v, want nil without backlog families", q)
	}
}

func TestAggregateAsyncOperationsByStatus(t *testing.T) {
	body := `hindsight_async_operations{operation_type="retain",status="pending"} 2
hindsight_async_operations{operation_type="consolidation",status="pending"} 5
hindsight_async_operations{operation_type="retain",status="failed"} 1
hindsight_async_operations{operation_type="reflect",status="mystery"} 7
hindsight_consolidation_backlog 8
hindsight_consolidation_failed 3
`
	_, _, q := aggregateMetrics(mustSamples(t, body))
	if q == nil {
		t.Fatal("Queue = nil, want populated")
	}
	if q.Pending == nil || *q.Pending != 7 {
		t.Errorf("Pending = %v, want 7 (summed across operation types)", q.Pending)
	}
	if q.Failed == nil || *q.Failed != 1 {
		t.Errorf("Failed = %v, want 1 (unknown statuses unclassified)", q.Failed)
	}
	if q.Processing != nil {
		t.Errorf("Processing = %v, want nil", q.Processing)
	}
	if q.ConsolidationBacklog == nil || *q.ConsolidationBacklog != 8 {
		t.Errorf("ConsolidationBacklog = %v, want 8", q.ConsolidationBacklog)
	}
	if q.ConsolidationFailed == nil || *q.ConsolidationFailed != 3 {
		t.Errorf("ConsolidationFailed = %v, want 3", q.ConsolidationFailed)
	}
}

// TestAggregateDropsIdentityLabels proves bank identifiers survive only as
// summed values: every series with an arbitrary bank label contributes to
// the same aggregate and the label itself is discarded.
func TestAggregateDropsIdentityLabels(t *testing.T) {
	body := `hindsight_llm_calls_total{provider="p",bank="b-secret",model="m"} 1
hindsight_llm_calls_total{provider="p",bank="b-other",model="m"} 2
`
	samples, err := parseSelectedMetrics(body)
	if err != nil {
		t.Fatalf("parseSelectedMetrics: %v", err)
	}
	for _, s := range samples {
		if _, ok := s.labels["bank"]; ok {
			t.Errorf("bank label retained on parsed sample: %+v", s.labels)
		}
	}
	if c := mustCounters(t, body); c.LLMCallsTotal == nil || *c.LLMCallsTotal != 3 {
		t.Errorf("LLMCallsTotal = %v, want 3", c.LLMCallsTotal)
	}
}
