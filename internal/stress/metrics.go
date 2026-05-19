package stress

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HistogramBucket is one cumulative bucket from a Prometheus histogram.
type HistogramBucket struct {
	UpperBound float64 // le="" value; +Inf as math.Inf(+1)
	Count      int64
}

// HistogramSnapshot captures the parts of a Prometheus histogram needed
// for quantile interpolation.
type HistogramSnapshot struct {
	Buckets []HistogramBucket // sorted by UpperBound
	Sum     float64
	Count   int64
}

// Snapshot holds the BNS counters and the query duration histogram at a
// single point in time. Only the metrics referenced in the report are
// extracted; extra metrics in the input are silently ignored.
type Snapshot struct {
	QueriesByOutcome map[string]int64 // sum across qtypes per outcome label
	UpstreamQueries  int64
	CoalescedQueries int64
	CacheEvictions   int64
	Panics           int64
	DurationHist     HistogramSnapshot
}

// MetricsDiff is the delta of two Snapshots, with derived quantities.
type MetricsDiff struct {
	QueriesByOutcome map[string]int64
	TotalQueries     int64
	UpstreamQueries  int64
	CoalescedQueries int64
	CacheEvictions   int64
	Panics           int64
	DurationHist     HistogramSnapshot
}

// CacheHitRate returns 1 - upstream/total. Returns 0 when total is zero.
func (d MetricsDiff) CacheHitRate() float64 {
	if d.TotalQueries == 0 {
		return 0
	}
	return 1.0 - float64(d.UpstreamQueries)/float64(d.TotalQueries)
}

// DurationQuantile interpolates a quantile from the diff's histogram. q
// is in [0, 1]. Returns 0 when the histogram is empty.
func (d MetricsDiff) DurationQuantile(q float64) time.Duration {
	h := d.DurationHist
	if h.Count == 0 {
		return 0
	}
	target := float64(h.Count) * q
	var prevBound float64
	var prevCount int64
	for _, b := range h.Buckets {
		if float64(b.Count) >= target {
			width := b.UpperBound - prevBound
			countIn := float64(b.Count - prevCount)
			if countIn <= 0 {
				return time.Duration(b.UpperBound * float64(time.Second))
			}
			frac := (target - float64(prevCount)) / countIn
			seconds := prevBound + frac*width
			return time.Duration(seconds * float64(time.Second))
		}
		prevBound = b.UpperBound
		prevCount = b.Count
	}
	return time.Duration(h.Buckets[len(h.Buckets)-1].UpperBound * float64(time.Second))
}

// ParseSnapshot reads a Prometheus text-exposition body and extracts the
// fields the stress harness cares about.
func ParseSnapshot(raw []byte) (Snapshot, error) {
	snap := Snapshot{QueriesByOutcome: map[string]int64{}}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, labels, value, err := parsePromLine(line)
		if err != nil {
			return snap, err
		}
		switch name {
		case "bns_queries_total":
			outcome := labels["outcome"]
			if outcome == "" {
				continue
			}
			snap.QueriesByOutcome[outcome] += int64(value)
		case "bns_upstream_queries_total":
			snap.UpstreamQueries += int64(value)
		case "bns_coalesced_queries_total":
			snap.CoalescedQueries += int64(value)
		case "bns_cache_evictions_total":
			snap.CacheEvictions += int64(value)
		case "bns_panics_total":
			snap.Panics += int64(value)
		case "bns_query_duration_seconds_bucket":
			ub := labels["le"]
			b, err := parseLE(ub)
			if err != nil {
				return snap, err
			}
			snap.DurationHist.Buckets = append(snap.DurationHist.Buckets,
				HistogramBucket{UpperBound: b, Count: int64(value)})
		case "bns_query_duration_seconds_sum":
			snap.DurationHist.Sum = value
		case "bns_query_duration_seconds_count":
			snap.DurationHist.Count = int64(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return snap, err
	}

	// Insertion sort — bucket count is small (typically ≤ 20).
	for i := 1; i < len(snap.DurationHist.Buckets); i++ {
		for j := i; j > 0 && snap.DurationHist.Buckets[j-1].UpperBound > snap.DurationHist.Buckets[j].UpperBound; j-- {
			snap.DurationHist.Buckets[j-1], snap.DurationHist.Buckets[j] = snap.DurationHist.Buckets[j], snap.DurationHist.Buckets[j-1]
		}
	}
	return snap, nil
}

// FetchSnapshot scrapes <baseURL>/metrics and returns the parsed Snapshot.
// baseURL has the form "http://host:port".
func FetchSnapshot(client *http.Client, baseURL string) (Snapshot, []byte, error) {
	resp, err := client.Get(baseURL + "/metrics")
	if err != nil {
		return Snapshot{}, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Snapshot{}, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return Snapshot{}, body, fmt.Errorf("metrics status %d", resp.StatusCode)
	}
	snap, err := ParseSnapshot(body)
	return snap, body, err
}

// Diff returns after - before for every recorded counter.
func Diff(before, after Snapshot) MetricsDiff {
	d := MetricsDiff{QueriesByOutcome: map[string]int64{}}
	for k, v := range after.QueriesByOutcome {
		d.QueriesByOutcome[k] = v - before.QueriesByOutcome[k]
		d.TotalQueries += d.QueriesByOutcome[k]
	}
	d.UpstreamQueries = after.UpstreamQueries - before.UpstreamQueries
	d.CoalescedQueries = after.CoalescedQueries - before.CoalescedQueries
	d.CacheEvictions = after.CacheEvictions - before.CacheEvictions
	d.Panics = after.Panics - before.Panics

	d.DurationHist = diffHistogram(before.DurationHist, after.DurationHist)
	return d
}

func diffHistogram(before, after HistogramSnapshot) HistogramSnapshot {
	if len(before.Buckets) != len(after.Buckets) {
		return HistogramSnapshot{
			Buckets: append([]HistogramBucket(nil), after.Buckets...),
			Sum:     after.Sum - before.Sum,
			Count:   after.Count - before.Count,
		}
	}
	out := HistogramSnapshot{
		Buckets: make([]HistogramBucket, len(after.Buckets)),
		Sum:     after.Sum - before.Sum,
		Count:   after.Count - before.Count,
	}
	for i := range after.Buckets {
		out.Buckets[i] = HistogramBucket{
			UpperBound: after.Buckets[i].UpperBound,
			Count:      after.Buckets[i].Count - before.Buckets[i].Count,
		}
	}
	return out
}

func parsePromLine(line string) (name string, labels map[string]string, value float64, err error) {
	labels = map[string]string{}
	openBrace := strings.IndexByte(line, '{')
	spaceIdx := strings.LastIndexByte(line, ' ')
	if spaceIdx < 0 {
		return "", nil, 0, fmt.Errorf("malformed line: %q", line)
	}
	valueStr := line[spaceIdx+1:]
	v, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		return "", nil, 0, fmt.Errorf("bad value in %q: %w", line, err)
	}
	value = v

	if openBrace == -1 {
		name = line[:spaceIdx]
		return name, labels, value, nil
	}
	name = line[:openBrace]
	closeBrace := strings.IndexByte(line, '}')
	if closeBrace == -1 {
		return "", nil, 0, fmt.Errorf("missing } in %q", line)
	}
	labelBody := line[openBrace+1 : closeBrace]
	for _, pair := range splitLabels(labelBody) {
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(pair[:eq])
		val := strings.Trim(strings.TrimSpace(pair[eq+1:]), "\"")
		labels[key] = val
	}
	return name, labels, value, nil
}

// splitLabels splits a Prometheus label block on commas that are not
// inside a quoted value.
func splitLabels(body string) []string {
	var out []string
	depth := 0
	last := 0
	for i, ch := range body {
		switch ch {
		case '"':
			depth ^= 1
		case ',':
			if depth == 0 {
				out = append(out, body[last:i])
				last = i + 1
			}
		}
	}
	if last < len(body) {
		out = append(out, body[last:])
	}
	return out
}

func parseLE(s string) (float64, error) {
	if s == "+Inf" {
		return math.Inf(+1), nil
	}
	return strconv.ParseFloat(s, 64)
}
