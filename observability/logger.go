package observability

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/lmittmann/tint"
	"go.opentelemetry.io/otel/trace"
)

// NewLogger returns a configured slog.Logger.
// env="production" → JSON handler on stdout; anything else → coloured tint handler on stderr.
func NewLogger(env string) *slog.Logger {
	var handler slog.Handler
	if strings.EqualFold(env, "production") {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelDebug,
			ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
				if a.Key == slog.TimeKey {
					a.Value = slog.StringValue(time.Now().UTC().Format(time.RFC3339Nano))
				}
				return a
			},
		})
	} else {
		handler = tint.NewHandler(os.Stderr, &tint.Options{
			Level:      slog.LevelDebug,
			TimeFormat: time.TimeOnly,
		})
	}
	return slog.New(&traceHandler{handler})
}

// WithLogger stores l in ctx.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, l)
}

// LoggerFrom retrieves the logger from ctx; falls back to slog.Default().
func LoggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// Truncate shortens s to at most n bytes, appending "…" if cut.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// traceHandler wraps any slog.Handler and auto-injects OTel trace_id + span_id.
type traceHandler struct{ slog.Handler }

func (h *traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanFromContext(ctx).SpanContext(); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{h.Handler.WithGroup(name)}
}
