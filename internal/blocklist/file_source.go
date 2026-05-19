package blocklist

import (
	"bufio"
	"context"
	"fmt"
	"os"
)

// FileSource reads a blocklist from a local file on disk.
// One entry per line. Comments and hosts-format are accepted (see ParseLine).
type FileSource struct {
	Path string
}

// Load reads the file, parses each line, and returns the unique FQDNs
// the file contains. Malformed lines are dropped silently here; the
// loader (Task 11) reports aggregate parse-error counts.
func (s FileSource) Load(ctx context.Context) ([]string, error) {
	f, err := os.Open(s.Path)
	if err != nil {
		return nil, fmt.Errorf("open blocklist %q: %w", s.Path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Some blocklists have long lines; bump the buffer ceiling.
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
		return nil, fmt.Errorf("read blocklist %q: %w", s.Path, err)
	}
	return out, nil
}
