package builtin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nareg/goagent/tools/builtin"
)

func TestHTTPTool_get(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`)) //nolint:errcheck
	}))
	defer srv.Close()

	tool, err := builtin.NewHTTPTool()
	require.NoError(t, err)
	assert.Equal(t, "http", tool.Name())

	input, _ := json.Marshal(builtin.HTTPInput{URL: srv.URL})
	out, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)

	var result map[string]interface{}
	require.NoError(t, json.Unmarshal(out, &result))
	assert.Equal(t, float64(200), result["status_code"])
	assert.Contains(t, result["body"].(string), `"ok":true`)
}

func TestHTTPTool_post(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	tool, err := builtin.NewHTTPTool()
	require.NoError(t, err)

	input, _ := json.Marshal(builtin.HTTPInput{
		URL:    srv.URL,
		Method: http.MethodPost,
		Body:   `{"data":"value"}`,
		Headers: map[string]string{"Content-Type": "application/json"},
	})
	out, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)

	var result map[string]interface{}
	require.NoError(t, json.Unmarshal(out, &result))
	assert.Equal(t, float64(201), result["status_code"])
}

func TestHTTPTool_headersForwarded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer token123", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tool, err := builtin.NewHTTPTool()
	require.NoError(t, err)

	input, _ := json.Marshal(builtin.HTTPInput{
		URL:     srv.URL,
		Headers: map[string]string{"Authorization": "Bearer token123"},
	})
	_, err = tool.Execute(context.Background(), input)
	require.NoError(t, err)
}

func TestHTTPTool_emptyURL(t *testing.T) {
	tool, err := builtin.NewHTTPTool()
	require.NoError(t, err)

	input, _ := json.Marshal(builtin.HTTPInput{URL: ""})
	_, err = tool.Execute(context.Background(), input)
	assert.Error(t, err)
}

func TestHTTPTool_contextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hang forever
		<-r.Context().Done()
	}))
	defer srv.Close()

	tool, err := builtin.NewHTTPTool()
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	input, _ := json.Marshal(builtin.HTTPInput{URL: srv.URL})
	_, err = tool.Execute(ctx, input)
	assert.Error(t, err)
}

func TestHTTPTool_schema(t *testing.T) {
	tool, _ := builtin.NewHTTPTool()
	assert.True(t, json.Valid(tool.Schema()))
}
