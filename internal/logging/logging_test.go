package logging_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/bcrisp4/bns/internal/config"
	"github.com/bcrisp4/bns/internal/logging"
	"github.com/stretchr/testify/require"
)

func TestNew_JSONHandlerHonorsLevel(t *testing.T) {
	buf := &bytes.Buffer{}
	cfg := config.Logging{Level: "warn", Format: "json"}
	logger := logging.New(cfg, buf)

	logger.Info("ignored")
	logger.Warn("kept", slog.String("k", "v"))

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	require.Len(t, lines, 1, "info level entry should be filtered out")

	var got map[string]any
	require.NoError(t, json.Unmarshal(lines[0], &got))
	require.Equal(t, "WARN", got["level"])
	require.Equal(t, "kept", got["msg"])
	require.Equal(t, "v", got["k"])
}

func TestQueryLogger_DisabledIsNoOp(t *testing.T) {
	buf := &bytes.Buffer{}
	q := logging.QueryLogger(config.QueryLog{Enabled: false}, buf)
	q.LogQuery(slog.String("qname", "example.com."))
	require.Empty(t, buf.String())
}

func TestQueryLogger_EnabledWrites(t *testing.T) {
	buf := &bytes.Buffer{}
	q := logging.QueryLogger(config.QueryLog{Enabled: true}, buf)
	q.LogQuery(slog.String("qname", "example.com."))
	require.Contains(t, buf.String(), "example.com.")
}
