package observability_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/nareg/goagent/observability"
)

func TestLoggerFrom_fallback(t *testing.T) {
	l := observability.LoggerFrom(context.Background())
	assert.NotNil(t, l)
}

func TestWithLogger_roundtrip(t *testing.T) {
	l := slog.Default()
	ctx := observability.WithLogger(context.Background(), l)
	got := observability.LoggerFrom(ctx)
	assert.Equal(t, l, got)
}

func TestNewLogger_dev(t *testing.T) {
	l := observability.NewLogger("dev")
	assert.NotNil(t, l)
}

func TestNewLogger_production(t *testing.T) {
	l := observability.NewLogger("production")
	assert.NotNil(t, l)
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input string
		n     int
		want  string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello…"},
		{"", 5, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc…"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, observability.Truncate(tc.input, tc.n), "input=%q n=%d", tc.input, tc.n)
	}
}

func TestLoggerInContext_noPanic(t *testing.T) {
	l := observability.NewLogger("dev")
	ctx := observability.WithLogger(context.Background(), l)
	// Logging inside a context without an active span must not panic.
	observability.LoggerFrom(ctx).Info("test.event", slog.String("status", "ok"))
}
