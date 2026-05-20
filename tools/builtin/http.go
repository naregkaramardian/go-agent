package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nareg/goagent/tools"
)

const (
	httpDefaultTimeout = 30 * time.Second
	httpMaxBodyBytes   = 1 << 20 // 1 MiB
)

// HTTPInput is the input schema for HTTPTool.
type HTTPInput struct {
	URL     string            `json:"url"     description:"URL to request"`
	Method  string            `json:"method"  description:"HTTP method" optional:"true" enum:"GET,POST,PUT,PATCH,DELETE"`
	Body    string            `json:"body"    description:"Request body for POST/PUT/PATCH" optional:"true"`
	Headers map[string]string `json:"headers" description:"Additional HTTP headers" optional:"true"`
}

// HTTPTool makes HTTP requests and returns the response.
type HTTPTool struct {
	client *http.Client
	schema json.RawMessage
}

// NewHTTPTool constructs an HTTPTool with a default 30-second timeout.
func NewHTTPTool() (*HTTPTool, error) {
	s, err := tools.GenerateSchema(HTTPInput{})
	if err != nil {
		return nil, fmt.Errorf("builtin.NewHTTPTool: %w", err)
	}
	return &HTTPTool{
		client: &http.Client{Timeout: httpDefaultTimeout},
		schema: s,
	}, nil
}

func (t *HTTPTool) Name() string            { return "http" }
func (t *HTTPTool) Description() string     { return "Make an HTTP request and return the status code and body." }
func (t *HTTPTool) Schema() json.RawMessage { return t.schema }

func (t *HTTPTool) Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var in HTTPInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("http.Execute: parse input: %w", err)
	}
	if in.URL == "" {
		return nil, fmt.Errorf("http.Execute: url is required")
	}
	if in.Method == "" {
		in.Method = http.MethodGet
	}

	var bodyReader io.Reader
	if in.Body != "" {
		bodyReader = strings.NewReader(in.Body)
	}

	req, err := http.NewRequestWithContext(ctx, in.Method, in.URL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("http.Execute: build request: %w", err)
	}
	for k, v := range in.Headers {
		req.Header.Set(k, v)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http.Execute: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, httpMaxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("http.Execute: read body: %w", err)
	}

	out, _ := json.Marshal(map[string]interface{}{
		"status_code": resp.StatusCode,
		"body":        string(body),
	})
	return out, nil
}
