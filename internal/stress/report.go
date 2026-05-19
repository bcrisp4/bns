package stress

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ReportInput is the fully-resolved set of values used to render the
// per-run report.md. All formatting decisions (rounding, units, etc.)
// happen inside Render.
type ReportInput struct {
	Scenario    string
	StartedAt   time.Time
	Target      string
	Admin       string
	BNSGitSha   string
	GoVersion   string
	Host        string
	Duration    time.Duration
	Concurrency uint32

	TotalQueries int64
	QPS          float64
	P50          time.Duration
	P95          time.Duration
	P99          time.Duration
	IOErrors     int64
	IDMismatches int64

	Counters         map[string]int64
	UpstreamQueries  int64
	CoalescedQueries int64
	CacheEvictions   int64
	Panics           int64

	PprofCPU  string
	PprofHeap string
}

// Render produces the human-readable report.md. Output is stable for a
// given input; used both as on-disk artefact and as a test fixture.
func Render(in ReportInput) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "# bns stress report — %s — %s\n", in.Scenario, in.StartedAt.UTC().Format("2006-01-02T15:04:05Z"))
	sb.WriteString("\n## Setup\n")
	fmt.Fprintf(&sb, "- target: %s\n", in.Target)
	fmt.Fprintf(&sb, "- admin: %s\n", in.Admin)
	fmt.Fprintf(&sb, "- bns: %s\n", in.BNSGitSha)
	fmt.Fprintf(&sb, "- go: %s\n", in.GoVersion)
	fmt.Fprintf(&sb, "- host: %s\n", in.Host)
	fmt.Fprintf(&sb, "- scenario: %s | duration: %s | concurrency: %d\n", in.Scenario, in.Duration, in.Concurrency)

	sb.WriteString("\n## Headline\n")
	sb.WriteString("| metric        | value      |\n")
	sb.WriteString("|---------------|------------|\n")
	fmt.Fprintf(&sb, "| sustained QPS | %-10s |\n", formatInt(int64(in.QPS)))
	fmt.Fprintf(&sb, "| p50 / p95 / p99 (client-side) | %s / %s / %s ms |\n",
		formatMillis(in.P50), formatMillis(in.P95), formatMillis(in.P99))
	fmt.Fprintf(&sb, "| total queries | %-10s |\n", formatInt(in.TotalQueries))
	fmt.Fprintf(&sb, "| io errors     | %-10d |\n", in.IOErrors)
	fmt.Fprintf(&sb, "| id mismatches | %-10d |\n", in.IDMismatches)

	sb.WriteString("\n## Outcome breakdown (bns perspective, after − before)\n")
	sb.WriteString("| outcome    | count    | %      |\n")
	sb.WriteString("|------------|----------|--------|\n")

	total := int64(0)
	for _, v := range in.Counters {
		total += v
	}
	keys := make([]string, 0, len(in.Counters))
	for k := range in.Counters {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return outcomeOrder(keys[i]) < outcomeOrder(keys[j])
	})
	for _, k := range keys {
		v := in.Counters[k]
		pct := 0.0
		if total > 0 {
			pct = 100.0 * float64(v) / float64(total)
		}
		fmt.Fprintf(&sb, "| %-10s | %-8s | %-6s |\n", k, formatInt(v), formatPct(pct))
	}

	sb.WriteString("\n## Internals\n")
	hitRate := 0.0
	if in.TotalQueries > 0 {
		hitRate = 100.0 * (1.0 - float64(in.UpstreamQueries)/float64(in.TotalQueries))
	}
	fmt.Fprintf(&sb, "- upstream queries: %s   (cache hit rate = %s%%)\n", formatInt(in.UpstreamQueries), formatPct(hitRate))
	fmt.Fprintf(&sb, "- coalesced queries: %s\n", formatInt(in.CoalescedQueries))
	fmt.Fprintf(&sb, "- cache evictions: %s\n", formatInt(in.CacheEvictions))
	fmt.Fprintf(&sb, "- panics: %s\n", formatInt(in.Panics))

	if in.PprofCPU != "" || in.PprofHeap != "" {
		sb.WriteString("\n## Profiles\n")
		if in.PprofCPU != "" {
			fmt.Fprintf(&sb, "- CPU: %s   — `go tool pprof -top dist/stress/<this-dir>/%s`\n", in.PprofCPU, in.PprofCPU)
		}
		if in.PprofHeap != "" {
			fmt.Fprintf(&sb, "- Heap: %s — `go tool pprof -top dist/stress/<this-dir>/%s`\n", in.PprofHeap, in.PprofHeap)
		}
	}

	return sb.String()
}

func outcomeOrder(name string) int {
	switch name {
	case "forwarded":
		return 0
	case "blocked":
		return 1
	case "nxdomain":
		return 2
	case "error":
		return 3
	default:
		return 99
	}
}

func formatInt(n int64) string {
	s := fmt.Sprintf("%d", n)
	if n < 1000 && n > -1000 {
		return s
	}
	neg := false
	if s[0] == '-' {
		neg = true
		s = s[1:]
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

func formatPct(p float64) string {
	return fmt.Sprintf("%.1f", p)
}

func formatMillis(d time.Duration) string {
	return fmt.Sprintf("%.1f", float64(d)/float64(time.Millisecond))
}
