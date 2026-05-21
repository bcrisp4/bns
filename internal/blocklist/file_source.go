package blocklist

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
)

// FileSource reads a blocklist from a local file on disk.
// One entry per line. Comments and hosts-format are accepted (see ParseLine).
type FileSource struct {
	Path string
}

// Load reads the file, parses each line, and returns the unique FQDNs
// the file contains. Malformed lines are dropped silently here; the
// loader reports aggregate parse-error counts.
func (s FileSource) Load(ctx context.Context) ([]string, error) {
	f, err := os.Open(s.Path)
	if err != nil {
		return nil, fmt.Errorf("open blocklist %q: %w", s.Path, err)
	}
	defer f.Close()

	out, err := parseReader(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("read blocklist %q: %w", s.Path, err)
	}
	return out, nil
}

// parseReader reads newline-delimited blocklist lines from r, runs each
// through ParseLine, and returns the surviving FQDNs. Long lines up to
// 1 MiB are tolerated (some blocklists pack hostnames into wide rows).
func parseReader(ctx context.Context, r io.Reader) ([]string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	out := make([]string, 0, 1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if d, ok := ParseLine(scanner.Text()); ok {
			out = append(out, d)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
