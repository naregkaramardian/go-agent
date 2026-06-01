package cli

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"

	"github.com/nareg/goagent/agents"
	"github.com/nareg/goagent/observability"
	"github.com/nareg/goagent/orchestration"
)

var serverCmd = newServerCmd()

func newServerCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "server",
		Short:        "Run the agent as an HTTP API server",
		Long:         "Starts an authenticated REST + SSE HTTP server on --api-addr (default :8080).",
		SilenceUsage: true,
		RunE:         runServer,
	}
}

func runServer(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	ctx, deps, err := bootstrap(ctx)
	if err != nil {
		return err
	}
	defer deps.cleanup()

	if deps.newAgent == nil {
		return fmt.Errorf("no API key found; set ANTHROPIC_API_KEY or OPENAI_API_KEY")
	}

	apiKey := flags.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("API_KEY")
	}
	if apiKey == "" {
		return fmt.Errorf("--api-key or API_KEY env var is required (unauthenticated mode is not allowed)")
	}

	gin.SetMode(gin.ReleaseMode)
	router := buildRouter(deps, apiKey)

	srv := &http.Server{
		Addr:              flags.apiAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // SSE streams need no server-level write deadline
		IdleTimeout:       120 * time.Second,
	}

	deps.logger.InfoContext(ctx, "api.server.starting", slog.String("addr", flags.apiAddr))

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-sigCtx.Done():
		deps.logger.InfoContext(ctx, "api.server.shutdown.started")
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if shutErr := srv.Shutdown(shutCtx); shutErr != nil {
			deps.logger.Error("api.server.shutdown.error", slog.String("error", shutErr.Error()))
		}
		deps.logger.InfoContext(ctx, "api.server.shutdown.complete")
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("api.server: %w", err)
	}
}

func buildRouter(deps *infraDeps, apiKey string) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(requestLogger(deps.logger))

	r.GET("/health", handleHealth)

	v1 := r.Group("/v1")
	v1.Use(bearerAuth(apiKey))
	{
		v1.POST("/ask", handleAsk(deps))
		v1.POST("/stream", handleStream(deps))
		v1.POST("/orchestrate", handleOrchestrate(deps))
		v1.GET("/agents", handleAgents())
		v1.GET("/workflows", handleWorkflows())
	}
	return r
}

// ── Middleware ────────────────────────────────────────────────────────────────

func bearerAuth(apiKey string) gin.HandlerFunc {
	want := []byte("Bearer " + apiKey)
	return func(c *gin.Context) {
		got := []byte(c.GetHeader("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}

func requestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		dur := time.Since(start)
		status := c.Writer.Status()
		// SOC2: never log request bodies — method/path/status/latency only.
		logger.InfoContext(c.Request.Context(), "http.request",
			slog.String("method", c.Request.Method),
			slog.String("path", c.FullPath()),
			slog.Int("status", status),
			slog.Int64("latency_ms", dur.Milliseconds()),
		)
		observability.HTTPRequestsTotal.WithLabelValues(
			c.Request.Method, c.FullPath(), strconv.Itoa(status)).Inc()
		observability.HTTPRequestDuration.WithLabelValues(
			c.Request.Method, c.FullPath()).Observe(dur.Seconds())
	}
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func handleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

type askRequest struct {
	Message string `json:"message" binding:"required"`
}

type askResponse struct {
	Output  string  `json:"output"`
	Steps   int     `json:"steps"`
	CostUSD float64 `json:"cost_usd"`
	Model   string  `json:"model"`
}

func handleAsk(deps *infraDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req askRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
		defer cancel()
		ctx = observability.WithLogger(ctx, deps.logger)
		ctx = observability.WithCostLedger(ctx, observability.NewCostLedger())

		result, err := deps.newAgent().Run(ctx, req.Message)
		if err != nil {
			deps.logger.ErrorContext(ctx, "api.ask.error", slog.String("error", err.Error()))
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		model, _, _, _ := resolveAgentConfig(deps.provider)
		c.JSON(http.StatusOK, askResponse{
			Output:  result.Output,
			Steps:   result.Steps,
			CostUSD: result.Cost,
			Model:   model,
		})
	}
}

// sseChunk is the JSON envelope for every SSE data frame.
type sseChunk struct {
	Type    string  `json:"type"`
	Delta   string  `json:"delta,omitempty"`
	Steps   int     `json:"steps,omitempty"`
	CostUSD float64 `json:"cost_usd,omitempty"`
	Message string  `json:"message,omitempty"`
}

func handleStream(deps *infraDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req askRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
		defer cancel()
		ctx = observability.WithLogger(ctx, deps.logger)
		ctx = observability.WithCostLedger(ctx, observability.NewCostLedger())

		c.Header("X-Accel-Buffering", "no")
		streamAgent(c, ctx, func(pw *io.PipeWriter) (*agentResult, error) {
			result, err := deps.newAgent().RunStreaming(ctx, req.Message, pw)
			if err != nil {
				deps.logger.ErrorContext(ctx, "api.stream.error", slog.String("error", err.Error()))
				return nil, err
			}
			return &agentResult{Steps: result.Steps, Cost: result.Cost}, nil
		})
	}
}

type orchestrateRequest struct {
	Workflow string `json:"workflow" binding:"required"`
	Task     string `json:"task"     binding:"required"`
}

func handleOrchestrate(deps *infraDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req orchestrateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		wf, ok := orchestration.WorkflowByID(req.Workflow)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unknown workflow %q", req.Workflow)})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Minute)
		defer cancel()
		ctx = observability.WithLogger(ctx, deps.logger)
		ctx = observability.WithCostLedger(ctx, observability.NewCostLedger())

		c.Header("X-Accel-Buffering", "no")
		streamAgent(c, ctx, func(pw *io.PipeWriter) (*agentResult, error) {
			runner := wf.Build(deps.builder)
			result, err := runner.Run(ctx, req.Task, pw)
			if err != nil {
				deps.logger.ErrorContext(ctx, "api.orchestrate.error", slog.String("error", err.Error()))
				return nil, err
			}
			steps := 0
			for _, sr := range result.StepResults {
				steps += sr.Steps
			}
			return &agentResult{Steps: steps, Cost: result.TotalCost}, nil
		})
	}
}

func handleAgents() gin.HandlerFunc {
	type agentItem struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Tier        string `json:"tier"`
	}
	all := agents.All()
	items := make([]agentItem, len(all))
	for i, p := range all {
		items[i] = agentItem{
			ID: p.ID, Name: p.Name,
			Description: p.Description, Tier: string(p.RecommendedTier),
		}
	}
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, items)
	}
}

func handleWorkflows() gin.HandlerFunc {
	type wfItem struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	all := orchestration.AllWorkflows()
	items := make([]wfItem, len(all))
	for i, wf := range all {
		items[i] = wfItem{ID: wf.ID, Name: wf.Name, Description: wf.Description}
	}
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, items)
	}
}

// ── SSE helpers ───────────────────────────────────────────────────────────────

// agentResult carries the summary fields emitted in the SSE "done" frame.
type agentResult struct {
	Steps int
	Cost  float64
}

// streamAgent runs fn in a goroutine, pipes its output through an SSE stream
// on c. Each chunk written by fn becomes a "text" SSE frame; on completion a
// "done" frame is sent. A keepalive comment is sent every 15 s to prevent
// proxy timeouts.
func streamAgent(c *gin.Context, ctx context.Context, fn func(*io.PipeWriter) (*agentResult, error)) {
	chunks := make(chan sseChunk, 32)

	go func() {
		defer close(chunks)

		pr, pw := io.Pipe()

		// fnDone signals that fn has finished and carries its result.
		type fnOutcome struct {
			result *agentResult
			err    error
		}
		fnDone := make(chan fnOutcome, 1)
		go func() {
			result, err := fn(pw)
			if err != nil {
				pw.CloseWithError(err)
			} else {
				pw.Close()
			}
			fnDone <- fnOutcome{result, err}
		}()

		// Read all text chunks from the pipe before sending done/error.
		buf := make([]byte, 512)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				chunks <- sseChunk{Type: "text", Delta: string(buf[:n])}
			}
			if err != nil {
				break
			}
		}

		// Wait for fn to finish, then emit the terminal frame.
		out := <-fnDone
		if out.err != nil {
			chunks <- sseChunk{Type: "error", Message: "agent error"}
		} else if out.result != nil {
			chunks <- sseChunk{Type: "done", Steps: out.result.Steps, CostUSD: out.result.Cost}
		}
	}()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Status(http.StatusOK)

	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				return
			}
			b, _ := json.Marshal(chunk)
			fmt.Fprintf(c.Writer, "data: %s\n\n", b)
			c.Writer.Flush()
			if chunk.Type == "done" || chunk.Type == "error" {
				return
			}
		case <-time.After(15 * time.Second):
			fmt.Fprint(c.Writer, ": keepalive\n\n")
			c.Writer.Flush()
		case <-ctx.Done():
			return
		}
	}
}
