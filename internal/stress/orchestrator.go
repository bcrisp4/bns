package stress

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// AdminBaseURL returns "http://" + admin host:port. The orchestrator
// passes this into FetchSnapshot and the pprof helpers.
func AdminBaseURL(adminHostPort string) string {
	return "http://" + adminHostPort
}

// WaitForReady polls <baseURL>/readyz every interval until it returns
// HTTP 200, or ctx is cancelled / deadline exceeded.
func WaitForReady(ctx context.Context, baseURL string, interval time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/readyz", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("readyz not OK before deadline: %w", ctx.Err())
		case <-t.C:
		}
	}
}

// BNSEnv merges defaults with scenario overrides, returning a sorted
// slice of "KEY=value" strings ready for exec.Cmd.Env.
func BNSEnv(scenario, defaults map[string]string) []string {
	merged := map[string]string{}
	for k, v := range defaults {
		merged[k] = v
	}
	for k, v := range scenario {
		merged[k] = v
	}
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, fmt.Sprintf("%s=%s", k, v))
	}
	sort.Strings(out)
	return out
}
