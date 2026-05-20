package observability

type contextKey int

const (
	loggerKey     contextKey = iota
	costLedgerKey contextKey = iota
)
