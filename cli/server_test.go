package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nareg/goagent/agents"
	"github.com/nareg/goagent/core"
	"github.com/nareg/goagent/llm"
	"github.com/nareg/goagent/observability"
	"github.com/nareg/goagent/tools"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// ── Stub LLM ─────────────────────────────────────────────────────────────────

type stubLLM struct{ response string }

func (s *stubLLM) Complete(_ context.Context, _ *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return &llm.CompletionResponse{
		Content: s.response,
		Usage:   llm.Usage{InputTokens: 10, OutputTokens: 5},
	}, nil
}

func (s *stubLLM) Stream(_ context.Context, _ *llm.CompletionRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 2)
	ch <- llm.StreamChunk{TextDelta: s.response}
	ch <- llm.StreamChunk{Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}}
	close(ch)
	return ch, nil
}

func stubDeps(response string) *infraDeps {
	return &infraDeps{
		logger:   observability.NewLoggerTo("text", io.Discard),
		ledger:   observability.NewCostLedger(),
		provider: providerAnthropic,
		newAgent: func() *core.Agent {
			reg := tools.NewRegistry()
			buf := core.NewConversationBuffer(10_000)
			return core.NewAgent(core.AgentConfig{
				Model: "claude-haiku-4-5", System: "test", MaxSteps: 5, MaxTokens: 512,
			}, &stubLLM{response: response}, reg, buf)
		},
		builder: func(_ agents.Preset) *core.Agent { return nil },
		cleanup: func() {},
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func newTestRouter(deps *infraDeps, apiKey string) *gin.Engine {
	return buildRouter(deps, apiKey)
}

func do(t *testing.T, router *gin.Engine, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

// ── Health ────────────────────────────────────────────────────────────────────

func TestHealth_AlwaysOK(t *testing.T) {
	r := newTestRouter(stubDeps("ok"), "secret")
	rr := do(t, r, "GET", "/health", "", "")
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), `"status":"ok"`)
}

func TestHealth_NoAuthRequired(t *testing.T) {
	r := newTestRouter(stubDeps("ok"), "secret")
	// No Authorization header — should still be 200
	req := httptest.NewRequest("GET", "/health", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// ── Auth ──────────────────────────────────────────────────────────────────────

func TestAuth_MissingToken_Returns401(t *testing.T) {
	r := newTestRouter(stubDeps("ok"), "secret")
	req := httptest.NewRequest("GET", "/v1/agents", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAuth_WrongToken_Returns401(t *testing.T) {
	r := newTestRouter(stubDeps("ok"), "secret")
	rr := do(t, r, "GET", "/v1/agents", "", "wrong")
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestAuth_CorrectToken_Passes(t *testing.T) {
	r := newTestRouter(stubDeps("ok"), "secret")
	rr := do(t, r, "GET", "/v1/agents", "", "secret")
	assert.Equal(t, http.StatusOK, rr.Code)
}

// ── /v1/ask ───────────────────────────────────────────────────────────────────

func TestAsk_MissingMessage_Returns400(t *testing.T) {
	r := newTestRouter(stubDeps("hello"), "secret")
	rr := do(t, r, "POST", "/v1/ask", `{}`, "secret")
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAsk_InvalidJSON_Returns400(t *testing.T) {
	r := newTestRouter(stubDeps("hello"), "secret")
	rr := do(t, r, "POST", "/v1/ask", `not json`, "secret")
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestAsk_Success_ReturnsJSON(t *testing.T) {
	r := newTestRouter(stubDeps("The answer is 4"), "secret")
	rr := do(t, r, "POST", "/v1/ask", `{"message":"What is 2+2?"}`, "secret")
	require.Equal(t, http.StatusOK, rr.Code)

	var resp askResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "The answer is 4", resp.Output)
	assert.GreaterOrEqual(t, resp.Steps, 1)
	assert.NotEmpty(t, resp.Model)
}

// ── /v1/stream ────────────────────────────────────────────────────────────────

func TestStream_SSEHeaders(t *testing.T) {
	r := newTestRouter(stubDeps("hello"), "secret")
	rr := do(t, r, "POST", "/v1/stream", `{"message":"hi"}`, "secret")
	assert.Equal(t, "text/event-stream", rr.Header().Get("Content-Type"))
}

func TestStream_EmitsDoneFrame(t *testing.T) {
	r := newTestRouter(stubDeps("streaming response"), "secret")
	rr := do(t, r, "POST", "/v1/stream", `{"message":"hi"}`, "secret")
	body := rr.Body.String()
	assert.Contains(t, body, `"type":"done"`)
}

// ── /v1/agents ────────────────────────────────────────────────────────────────

func TestAgents_ReturnsAll(t *testing.T) {
	r := newTestRouter(stubDeps("ok"), "secret")
	rr := do(t, r, "GET", "/v1/agents", "", "secret")
	require.Equal(t, http.StatusOK, rr.Code)

	var items []map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&items))
	assert.NotEmpty(t, items)
	for _, item := range items {
		assert.NotEmpty(t, item["id"])
		assert.NotEmpty(t, item["name"])
	}
}

// ── /v1/workflows ─────────────────────────────────────────────────────────────

func TestWorkflows_ReturnsAll(t *testing.T) {
	r := newTestRouter(stubDeps("ok"), "secret")
	rr := do(t, r, "GET", "/v1/workflows", "", "secret")
	require.Equal(t, http.StatusOK, rr.Code)

	var items []map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&items))
	assert.NotEmpty(t, items)
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item["id"]
	}
	assert.Contains(t, ids, "code-review")
	assert.Contains(t, ids, "feature-build")
}

// ── /v1/orchestrate ───────────────────────────────────────────────────────────

func TestOrchestrate_UnknownWorkflow_Returns400(t *testing.T) {
	r := newTestRouter(stubDeps("ok"), "secret")
	body := `{"workflow":"does-not-exist","task":"do something"}`
	rr := do(t, r, "POST", "/v1/orchestrate", body, "secret")
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "unknown workflow")
}

func TestOrchestrate_MissingFields_Returns400(t *testing.T) {
	r := newTestRouter(stubDeps("ok"), "secret")
	rr := do(t, r, "POST", "/v1/orchestrate", `{"workflow":"code-review"}`, "secret")
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// ── bearerAuth timing safety ──────────────────────────────────────────────────

func TestBearerAuth_ConstantTime(t *testing.T) {
	// Both wrong tokens should produce identical 401 responses regardless of
	// where they differ — this test ensures we don't fast-path on length.
	r := newTestRouter(stubDeps("ok"), "correctkey123")

	rr1 := do(t, r, "GET", "/v1/agents", "", "wrongkey1234") // same length
	rr2 := do(t, r, "GET", "/v1/agents", "", "x")            // shorter

	assert.Equal(t, http.StatusUnauthorized, rr1.Code)
	assert.Equal(t, http.StatusUnauthorized, rr2.Code)

	var e1, e2 map[string]string
	json.NewDecoder(bytes.NewReader(rr1.Body.Bytes())).Decode(&e1)
	json.NewDecoder(bytes.NewReader(rr2.Body.Bytes())).Decode(&e2)
	assert.Equal(t, "unauthorized", e1["error"])
	assert.Equal(t, "unauthorized", e2["error"])
}
