package hindsight

import (
	"math"
	"strconv"
	"strings"

	"git.skobk.in/skobkin/aimonitor/internal/source"
)

// selectedFamilies is the closed set of Prometheus exposition families S5
// consumes (docs/S5_HINDSIGHT.md §4). Everything else is skipped at parse
// time; absent families are absence, never zero.
var selectedFamilies = map[string]bool{
	"hindsight_operation_total":           true,
	"hindsight_llm_calls_total":           true,
	"hindsight_llm_tokens_input_total":    true,
	"hindsight_llm_tokens_output_total":   true,
	"hindsight_http_requests_total":       true,
	"hindsight_http_requests_in_progress": true,
	"hindsight_db_pool_size":              true,
	"hindsight_db_pool_idle":              true,
	"hindsight_db_pool_min":               true,
	"hindsight_db_pool_max":               true,
	"hindsight_db_pool_waiting":           true,
	"hindsight_async_operations":          true,
	"hindsight_consolidation_backlog":     true,
	"hindsight_consolidation_failed":      true,
}

// metricSample is one parsed exposition sample. Only the four label keys
// S5 classifies by survive; every other label — bank IDs, providers,
// models, tenants — is dropped at parse time so it can never reach the
// snapshot.
type metricSample struct {
	name   string
	labels map[string]string
	value  float64
}

// metricsCounters is one aggregate counter sample: every nil pointer means
// the family had no series in this scrape (counters are created upstream
// only after relevant activity). This is the shape the rate baseline
// stores between refreshes.
type metricsCounters struct {
	RetainTotal      *float64
	RecallTotal      *float64
	ReflectTotal     *float64
	ConsolidateTotal *float64

	LLMCallsTotal     *float64
	InputTokensTotal  *float64
	OutputTokensTotal *float64

	HTTPRequestsTotal *float64
	HTTP5xxTotal      *float64
}

// metricsGauges holds the current gauge values of one scrape.
// DBPoolMin is retained but not displayed (the UI derives active from
// size-idle and caps with max).
type metricsGauges struct {
	DBPoolSize     *float64
	DBPoolIdle     *float64
	DBPoolMin      *float64
	DBPoolMax      *float64
	DBPoolWaiting  *float64
	HTTPInProgress *float64
}

// queueGauges aggregates the optional backlog families. Each field is
// independently optional; the aggregateMetrics caller receives nil when
// none of the families appeared at all.
type queueGauges struct {
	Pending    *float64
	Processing *float64
	Failed     *float64

	ConsolidationBacklog *float64
	ConsolidationFailed  *float64
}

// parseSelectedMetrics parses the exposition text into samples of the
// selected families. Line-oriented: comments and HELP/TYPE lines are
// skipped. Every other line must be structurally valid exposition —
// whatever its family — and a body that is not returns an error, so a
// truncated scrape or a proxy error page cannot pass for a successful
// one: the rate baseline must advance only after /metrics parses
// successfully (S5_IMPLEMENTATION_SPEC §6). Absence stays absence:
// samples of unselected families are skipped, and a selected sample with
// a non-finite value (Prometheus's "no data" marker) is skipped rather
// than becoming a zero. An optional trailing sample timestamp is ignored.
func parseSelectedMetrics(body string) ([]metricSample, error) {
	var samples []metricSample
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		s, ok := parseSampleLine(line)
		if !ok {
			return nil, &source.Error{
				Category: source.CatInvalidResponse,
				Detail:   "metrics response is not valid Prometheus exposition",
			}
		}
		if !selectedFamilies[s.name] || !isFinite(s.value) {
			continue
		}
		samples = append(samples, s)
	}
	return samples, nil
}

// parseSampleLine structurally parses one exposition line:
//
//	name{label="value",...} value [timestamp]
//	name value [timestamp]
//
// ok is false for anything not shaped like an exposition sample,
// regardless of which family the line belongs to.
func parseSampleLine(line string) (metricSample, bool) {
	name, rest := cutIdent(line)

	s := metricSample{name: name}
	if strings.HasPrefix(rest, "{") {
		labels, remainder, ok := parseLabels(rest)
		if !ok {
			return metricSample{}, false
		}
		s.labels = classifyLabels(labels)
		rest = remainder
	}

	fields := strings.Fields(rest)
	if len(fields) == 0 || len(fields) > 2 {
		return metricSample{}, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return metricSample{}, false
	}
	s.value = v
	return s, true
}

// cutIdent splits the metric name (up to the first '{' or space) from the
// rest of the line.
func cutIdent(line string) (name, rest string) {
	if i := strings.IndexAny(line, "{ "); i >= 0 {
		return line[:i], line[i:]
	}
	return line, ""
}

// parseLabels parses a `{a="b",c="d"}` prefix. Returns the labels and the
// remainder after the closing brace. Escaped characters in label values
// (\\, \", \n) are unescaped; anything malformed fails the whole line.
func parseLabels(s string) (labels map[string]string, remainder string, ok bool) {
	// s starts with '{'.
	s = s[1:]
	for {
		s = strings.TrimLeft(s, " ")
		if s == "" {
			return nil, "", false
		}
		if s[0] == '}' {
			return labels, s[1:], true
		}
		eq := strings.IndexByte(s, '=')
		if eq <= 0 {
			return nil, "", false
		}
		name := strings.TrimRight(s[:eq], " ")
		s = strings.TrimLeft(s[eq+1:], " ")
		if len(s) == 0 || s[0] != '"' {
			return nil, "", false
		}
		value, rest, valid := scanQuoted(s)
		if !valid {
			return nil, "", false
		}
		if labels == nil {
			labels = make(map[string]string)
		}
		labels[name] = value
		s = rest
		if strings.HasPrefix(s, ",") {
			s = s[1:]
			continue
		}
		if strings.HasPrefix(s, "}") {
			return labels, s[1:], true
		}
		return nil, "", false
	}
}

// scanQuoted reads one quoted label value starting at the opening quote,
// unescaping \\, \" and \n. Returns the value and the text after the
// closing quote.
func scanQuoted(s string) (value, rest string, ok bool) {
	// s starts with '"'.
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			if i+1 >= len(s) {
				return "", "", false
			}
			i++
			switch s[i] {
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			case 'n':
				b.WriteByte('\n')
			default:
				return "", "", false
			}
		case '"':
			return b.String(), s[i+1:], true
		default:
			b.WriteByte(c)
		}
	}
	return "", "", false
}

// classifyingLabels is the closed set of label keys aggregation reads.
// Every other label (bank IDs, providers, models, tenants) is discarded
// before the sample is even built.
var classifyingLabels = map[string]bool{
	"operation":      true,
	"operation_type": true,
	"status":         true,
	"status_class":   true,
}

// classifyLabels filters parsed labels down to the classifying keys,
// returning nil when none survived.
func classifyLabels(in map[string]string) map[string]string {
	var out map[string]string
	for k, v := range in {
		if classifyingLabels[k] {
			if out == nil {
				out = make(map[string]string, 1)
			}
			out[k] = v
		}
	}
	return out
}

// aggregateMetrics sums every exposed series of each family into one
// deployment-wide sample, regardless of any bank/provider/model labels
// the series carry (they are aggregated away, never retained). Families
// with no series stay nil.
func aggregateMetrics(samples []metricSample) (metricsCounters, metricsGauges, *queueGauges) {
	var counters metricsCounters
	var gauges metricsGauges
	var queue queueGauges
	queueSeen := false

	for _, s := range samples {
		switch s.name {
		case "hindsight_operation_total":
			switch s.labels["operation"] {
			case "retain":
				counters.RetainTotal = add(counters.RetainTotal, s.value)
			case "recall":
				counters.RecallTotal = add(counters.RecallTotal, s.value)
			case "reflect":
				counters.ReflectTotal = add(counters.ReflectTotal, s.value)
			case "consolidation":
				counters.ConsolidateTotal = add(counters.ConsolidateTotal, s.value)
			}
		case "hindsight_llm_calls_total":
			counters.LLMCallsTotal = add(counters.LLMCallsTotal, s.value)
		case "hindsight_llm_tokens_input_total":
			counters.InputTokensTotal = add(counters.InputTokensTotal, s.value)
		case "hindsight_llm_tokens_output_total":
			counters.OutputTokensTotal = add(counters.OutputTokensTotal, s.value)
		case "hindsight_http_requests_total":
			counters.HTTPRequestsTotal = add(counters.HTTPRequestsTotal, s.value)
			if s.labels["status_class"] == "5xx" {
				counters.HTTP5xxTotal = add(counters.HTTP5xxTotal, s.value)
			}
		case "hindsight_http_requests_in_progress":
			gauges.HTTPInProgress = add(gauges.HTTPInProgress, s.value)
		case "hindsight_db_pool_size":
			gauges.DBPoolSize = add(gauges.DBPoolSize, s.value)
		case "hindsight_db_pool_idle":
			gauges.DBPoolIdle = add(gauges.DBPoolIdle, s.value)
		case "hindsight_db_pool_max":
			gauges.DBPoolMax = add(gauges.DBPoolMax, s.value)
		case "hindsight_db_pool_min":
			gauges.DBPoolMin = add(gauges.DBPoolMin, s.value)
		case "hindsight_db_pool_waiting":
			gauges.DBPoolWaiting = add(gauges.DBPoolWaiting, s.value)
		case "hindsight_async_operations":
			switch s.labels["status"] {
			case "pending":
				queue.Pending = add(queue.Pending, s.value)
				queueSeen = true
			case "processing":
				queue.Processing = add(queue.Processing, s.value)
				queueSeen = true
			case "failed":
				queue.Failed = add(queue.Failed, s.value)
				queueSeen = true
			}
		case "hindsight_consolidation_backlog":
			queue.ConsolidationBacklog = add(queue.ConsolidationBacklog, s.value)
			queueSeen = true
		case "hindsight_consolidation_failed":
			queue.ConsolidationFailed = add(queue.ConsolidationFailed, s.value)
			queueSeen = true
		}
	}

	var queuePtr *queueGauges
	if queueSeen {
		queuePtr = &queue
	}
	return counters, gauges, queuePtr
}

// add accumulates v into acc, allocating on the first contribution.
func add(acc *float64, v float64) *float64 {
	if acc == nil {
		return &v
	}
	*acc += v
	return acc
}

// isFinite reports whether v is a usable sample value.
func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
