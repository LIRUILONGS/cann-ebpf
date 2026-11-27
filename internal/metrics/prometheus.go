package metrics

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ParsedSample is one parsed Prometheus family sample before timestamps and
// source identity are attached by the collector.
type ParsedSample struct {
	Name      string
	Kind      Kind
	Value     *float64
	Histogram *Histogram
	Unit      string
	Labels    map[string]string
}

// ParsePrometheusText parses the Prometheus text exposition format. It handles
// # TYPE/# HELP, labels (including escaped quotes), gauge/counter values and
// histogram _bucket/_sum/_count families. NaN/Inf samples are skipped and
// reported as warnings rather than turned into fabricated numbers.
func ParsePrometheusText(raw []byte, include []*regexp.Regexp) ([]ParsedSample, []string, error) {
	if len(raw) == 0 {
		return nil, nil, nil
	}
	lines := strings.Split(string(raw), "\n")
	types := make(map[string]string)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[0] == "#" && fields[1] == "TYPE" {
			types[fields[2]] = strings.ToLower(fields[3])
		}
	}

	var warnings []string
	var samples []ParsedSample
	histograms := make(map[string]*histogramAggregate)
	summaryWarned := make(map[string]struct{})
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rawSample, err := parsePrometheusLine(line)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("prometheus: %v", err))
			continue
		}
		family, kind := classifyMetric(rawSample.name, types)
		if !matchInclude(family, include) {
			continue
		}
		switch kind {
		case "histogram":
			handleHistogramSample(histograms, family, rawSample, &warnings)
		case "summary":
			if _, warned := summaryWarned[family]; !warned {
				warnings = append(warnings, fmt.Sprintf("prometheus: summary family %q is not supported", family))
				summaryWarned[family] = struct{}{}
			}
		case "counter", "gauge", "untyped":
			if math.IsNaN(rawSample.value) || math.IsInf(rawSample.value, 0) {
				warnings = append(warnings, fmt.Sprintf("prometheus: skip non-finite sample %q", rawSample.name))
				continue
			}
			if err := validateLabels(rawSample.labels); err != nil {
				warnings = append(warnings, fmt.Sprintf("prometheus: skip %q: %v", rawSample.name, err))
				continue
			}
			sampleKind := KindGauge
			if kind == "counter" {
				sampleKind = KindCounter
			}
			value := rawSample.value
			samples = append(samples, ParsedSample{
				Name: rawSample.name, Kind: sampleKind, Value: &value,
				Unit: inferUnit(rawSample.name), Labels: cloneLabels(rawSample.labels),
			})
		default:
			continue
		}
	}

	for _, aggregate := range histograms {
		sample, aggregateWarnings := finalizeHistogram(aggregate, include)
		warnings = append(warnings, aggregateWarnings...)
		if sample != nil {
			samples = append(samples, *sample)
		}
	}
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].Name != samples[j].Name {
			return samples[i].Name < samples[j].Name
		}
		return labelKey(samples[i].Labels) < labelKey(samples[j].Labels)
	})
	return samples, dedupeStrings(warnings), nil
}

type rawPrometheusSample struct {
	name   string
	labels map[string]string
	value  float64
}

func parsePrometheusLine(line string) (rawPrometheusSample, error) {
	var name, labelsText, rest string
	if open := strings.IndexByte(line, '{'); open >= 0 {
		name = strings.TrimSpace(line[:open])
		close := findClosingBrace(line, open)
		if close < 0 {
			return rawPrometheusSample{}, fmt.Errorf("unclosed label block in %q", line)
		}
		labelsText = line[open+1 : close]
		rest = strings.TrimSpace(line[close+1:])
	} else {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return rawPrometheusSample{}, fmt.Errorf("malformed sample %q", line)
		}
		name = fields[0]
		rest = strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
	}
	if !validMetricName(name) {
		return rawPrometheusSample{}, fmt.Errorf("invalid metric name %q", name)
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return rawPrometheusSample{}, fmt.Errorf("missing value in %q", line)
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return rawPrometheusSample{}, fmt.Errorf("invalid value in %q: %w", line, err)
	}
	labels := map[string]string{}
	if labelsText != "" {
		labels, err = parseLabelPairs(labelsText)
		if err != nil {
			return rawPrometheusSample{}, fmt.Errorf("labels in %q: %w", line, err)
		}
	}
	return rawPrometheusSample{name: name, labels: labels, value: value}, nil
}

func validMetricName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || r == ':' {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func findClosingBrace(line string, open int) int {
	inQuote := false
	escaped := false
	for i := open + 1; i < len(line); i++ {
		c := line[i]
		if escaped {
			escaped = false
			continue
		}
		if inQuote && c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			inQuote = !inQuote
			continue
		}
		if c == '}' && !inQuote {
			return i
		}
	}
	return -1
}

func parseLabelPairs(text string) (map[string]string, error) {
	labels := make(map[string]string)
	i := 0
	for i < len(text) {
		for i < len(text) && (text[i] == ' ' || text[i] == '\t' || text[i] == ',') {
			i++
		}
		if i >= len(text) {
			break
		}
		start := i
		for i < len(text) && text[i] != '=' {
			i++
		}
		if i >= len(text) {
			return nil, fmt.Errorf("label %q has no =", strings.TrimSpace(text[start:]))
		}
		key := strings.TrimSpace(text[start:i])
		if key == "" {
			return nil, fmt.Errorf("empty label key")
		}
		i++
		for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
			i++
		}
		if i >= len(text) || text[i] != '"' {
			return nil, fmt.Errorf("label %q value is not quoted", key)
		}
		i++
		var value strings.Builder
		closed := false
		for i < len(text) {
			c := text[i]
			if c == '\\' {
				i++
				if i >= len(text) {
					return nil, fmt.Errorf("label %q has trailing escape", key)
				}
				switch text[i] {
				case 'n':
					value.WriteByte('\n')
				default:
					value.WriteByte(text[i])
				}
				i++
				continue
			}
			if c == '"' {
				i++
				closed = true
				break
			}
			value.WriteByte(c)
			i++
		}
		if !closed {
			return nil, fmt.Errorf("label %q has unclosed quote", key)
		}
		labels[key] = value.String()
		for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
			i++
		}
		if i < len(text) && text[i] != ',' {
			return nil, fmt.Errorf("label %q: expected comma", key)
		}
	}
	return labels, nil
}

func classifyMetric(name string, types map[string]string) (string, string) {
	if kind, ok := types[name]; ok {
		return name, kind
	}
	for _, suffix := range []string{"_bucket", "_sum", "_count"} {
		if strings.HasSuffix(name, suffix) {
			family := strings.TrimSuffix(name, suffix)
			if kind, ok := types[family]; ok {
				return family, kind
			}
		}
	}
	if strings.HasSuffix(name, "_total") {
		return name, "counter"
	}
	return name, "gauge"
}

type histogramAggregate struct {
	family     string
	labels     map[string]string
	buckets    map[float64]float64
	sum        float64
	hasSum     bool
	count      float64
	hasCount   bool
	plusInf    float64
	hasPlusInf bool
}

func handleHistogramSample(aggregates map[string]*histogramAggregate, family string, sample rawPrometheusSample, warnings *[]string) {
	var suffix string
	switch {
	case strings.HasSuffix(sample.name, "_bucket"):
		suffix = "_bucket"
	case strings.HasSuffix(sample.name, "_sum"):
		suffix = "_sum"
	case strings.HasSuffix(sample.name, "_count"):
		suffix = "_count"
	default:
		*warnings = append(*warnings, fmt.Sprintf("prometheus: histogram family %q has an unsupported sample %q", family, sample.name))
		return
	}
	labels := cloneLabels(sample.labels)
	delete(labels, "le")
	key := family + "\x00" + labelKey(labels)
	aggregate := aggregates[key]
	if aggregate == nil {
		aggregate = &histogramAggregate{family: family, labels: labels, buckets: make(map[float64]float64)}
		aggregates[key] = aggregate
	}
	switch suffix {
	case "_bucket":
		le, ok := sample.labels["le"]
		if !ok {
			*warnings = append(*warnings, fmt.Sprintf("prometheus: histogram %q bucket has no le label", sample.name))
			return
		}
		bound, err := strconv.ParseFloat(le, 64)
		if err != nil {
			*warnings = append(*warnings, fmt.Sprintf("prometheus: histogram %q has invalid le=%q", sample.name, le))
			return
		}
		if math.IsInf(bound, 1) {
			aggregate.plusInf = sample.value
			aggregate.hasPlusInf = true
		} else {
			aggregate.buckets[bound] = sample.value
		}
	case "_sum":
		aggregate.sum = sample.value
		aggregate.hasSum = true
	case "_count":
		aggregate.count = sample.value
		aggregate.hasCount = true
	}
}

func finalizeHistogram(aggregate *histogramAggregate, include []*regexp.Regexp) (*ParsedSample, []string) {
	if !matchInclude(aggregate.family, include) {
		return nil, nil
	}
	var warnings []string
	if !aggregate.hasCount {
		if aggregate.hasPlusInf {
			aggregate.count = aggregate.plusInf
			aggregate.hasCount = true
		} else {
			return nil, []string{fmt.Sprintf("prometheus: histogram %q has no _count sample", aggregate.family)}
		}
	}
	if !aggregate.hasSum {
		return nil, []string{fmt.Sprintf("prometheus: histogram %q has no _sum sample", aggregate.family)}
	}
	count, err := integralCount(aggregate.count)
	if err != nil {
		return nil, []string{fmt.Sprintf("prometheus: histogram %q count: %v", aggregate.family, err)}
	}
	bounds := make([]float64, 0, len(aggregate.buckets))
	for bound := range aggregate.buckets {
		if math.IsNaN(bound) || math.IsInf(bound, 0) {
			continue
		}
		bounds = append(bounds, bound)
	}
	sort.Float64s(bounds)
	counts := make([]int64, 0, len(bounds))
	previous := int64(0)
	for i, bound := range bounds {
		value, err := integralCount(aggregate.buckets[bound])
		if err != nil {
			return nil, []string{fmt.Sprintf("prometheus: histogram %q bucket %v: %v", aggregate.family, bound, err)}
		}
		if i > 0 && value < previous {
			return nil, []string{fmt.Sprintf("prometheus: histogram %q has non-monotonic cumulative buckets", aggregate.family)}
		}
		previous = value
		counts = append(counts, value)
	}
	if aggregate.hasPlusInf {
		if plusCount, err := integralCount(aggregate.plusInf); err == nil && plusCount != count {
			warnings = append(warnings, fmt.Sprintf("prometheus: histogram %q +Inf bucket=%d does not match _count=%d", aggregate.family, plusCount, count))
		}
	}
	return &ParsedSample{
		Name:      aggregate.family,
		Kind:      KindHistogram,
		Histogram: &Histogram{Buckets: bounds, Counts: counts, Sum: aggregate.sum, Count: count, Cumulative: true},
		Unit:      inferUnit(aggregate.family),
		Labels:    cloneLabels(aggregate.labels),
	}, warnings
}

func integralCount(value float64) (int64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, fmt.Errorf("invalid count %v", value)
	}
	rounded := math.Round(value)
	if math.Abs(value-rounded) > 1e-6 {
		return 0, fmt.Errorf("non-integer count %v", value)
	}
	if rounded > math.MaxInt64 {
		return 0, fmt.Errorf("count %v overflows int64", value)
	}
	return int64(rounded), nil
}

func labelKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(labels[key])
		builder.WriteByte('\x00')
	}
	return builder.String()
}

func matchInclude(name string, include []*regexp.Regexp) bool {
	if len(include) == 0 {
		return true
	}
	for _, expression := range include {
		if expression.MatchString(name) {
			return true
		}
	}
	return false
}

func inferUnit(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "celsius") || strings.Contains(lower, "temperature"):
		return "celsius"
	case strings.Contains(lower, "watt") || strings.Contains(lower, "power"):
		return "watts"
	case strings.Contains(lower, "_bytes") || strings.HasSuffix(lower, "_bytes"):
		return "bytes"
	case strings.Contains(lower, "_seconds") || strings.HasSuffix(lower, "_seconds") || strings.HasSuffix(lower, "_s"):
		return "seconds"
	case strings.Contains(lower, "_percent") || strings.Contains(lower, "_perc") || strings.HasSuffix(lower, "_pct"):
		return "percent"
	case strings.Contains(lower, "_ratio") || strings.Contains(lower, "utilization"):
		return "ratio"
	default:
		return "count"
	}
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// CounterTracker detects counter resets between scrapes. It is deliberately
// stateless about the export format: callers pass a stable series key.
type CounterTracker struct {
	last map[string]float64
}

// NewCounterTracker creates an empty reset detector.
func NewCounterTracker() *CounterTracker {
	return &CounterTracker{last: make(map[string]float64)}
}

// Observe returns true when value moved backwards from the previous
// observation for the same key. The first observation is never a reset.
func (t *CounterTracker) Observe(key string, value float64) bool {
	if t == nil {
		return false
	}
	previous, ok := t.last[key]
	t.last[key] = value
	return ok && value < previous
}

// PrometheusConfig configures one Prometheus endpoint.
type PrometheusConfig struct {
	Name    string
	URL     string
	Include []string
	Client  *http.Client
}

// PrometheusSource scrapes one Prometheus text endpoint.
type PrometheusSource struct {
	name     string
	url      string
	include  []*regexp.Regexp
	client   *http.Client
	every    time.Duration
	deadline time.Duration
	tracker  *CounterTracker
}

// NewPrometheusSource creates a Prometheus source and validates its regex
// allowlist.
func NewPrometheusSource(config PrometheusConfig, interval, timeout time.Duration) (*PrometheusSource, error) {
	if strings.TrimSpace(config.URL) == "" {
		return nil, fmt.Errorf("prometheus: url is required")
	}
	if interval <= 0 {
		interval = time.Second
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = "prometheus"
	} else if !strings.HasPrefix(name, "prometheus:") {
		name = "prometheus:" + name
	}
	var include []*regexp.Regexp
	for _, pattern := range config.Include {
		expression, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("prometheus: invalid include %q: %w", pattern, err)
		}
		include = append(include, expression)
	}
	client := config.Client
	if client == nil {
		client = &http.Client{}
	}
	return &PrometheusSource{
		name: name, url: config.URL, include: include, client: client,
		every: interval, deadline: timeout, tracker: NewCounterTracker(),
	}, nil
}

func (s *PrometheusSource) Name() string            { return s.name }
func (s *PrometheusSource) Interval() time.Duration { return s.every }
func (s *PrometheusSource) Timeout() time.Duration  { return s.deadline }

func (s *PrometheusSource) Collect(ctx context.Context) ([]Point, []string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, nil, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("prometheus %s: %w", s.url, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("prometheus %s: HTTP %d", s.url, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024*1024))
	if err != nil {
		return nil, nil, fmt.Errorf("prometheus %s: read: %w", s.url, err)
	}
	parsed, warnings, err := ParsePrometheusText(body, s.include)
	if err != nil {
		return nil, warnings, err
	}
	points := make([]Point, 0, len(parsed))
	for _, sample := range parsed {
		switch sample.Kind {
		case KindCounter:
			if sample.Value != nil {
				key := SeriesKey(Point{Source: s.name, Name: sample.Name, Kind: KindCounter, Labels: sample.Labels})
				if s.tracker.Observe(key, *sample.Value) {
					warnings = append(warnings, fmt.Sprintf("prometheus %s: counter reset detected for %s", s.name, sample.Name))
				}
			}
		case KindHistogram:
			if sample.Histogram != nil {
				key := SeriesKey(Point{Source: s.name, Name: sample.Name, Kind: KindHistogram, Labels: sample.Labels})
				if s.tracker.Observe(key, float64(sample.Histogram.Count)) {
					warnings = append(warnings, fmt.Sprintf("prometheus %s: histogram counter reset detected for %s", s.name, sample.Name))
				}
			}
		}
		point := Point{
			Schema: SchemaV1, Source: s.name, Name: sample.Name, Kind: sample.Kind,
			Value: sample.Value, Unit: sample.Unit, Labels: cloneLabels(sample.Labels),
		}
		if sample.Histogram != nil {
			point.Histogram = sample.Histogram
		}
		points = append(points, point)
	}
	return points, dedupeStrings(warnings), nil
}
