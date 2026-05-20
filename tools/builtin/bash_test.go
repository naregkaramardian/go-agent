package builtin_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nareg/goagent/tools/builtin"
)

func TestBashTool_echo(t *testing.T) {
	tool, err := builtin.NewBashTool()
	require.NoError(t, err)
	assert.Equal(t, "bash", tool.Name())

	input, _ := json.Marshal(builtin.BashInput{Command: "echo hello"})
	out, err := tool.Execute(context.Background(), input)
	require.NoError(t, err)

	var result map[string]interface{}
	require.NoError(t, json.Unmarshal(out, &result))
	assert.Contains(t, result["stdout"].(string), "hello")
	assert.Equal(t, float64(0), result["exit_code"])
}

func TestBashTool_exitCode(t *testing.T) {
	tool, err := builtin.NewBashTool()
	require.NoError(t, err)

	input, _ := json.Marshal(builtin.BashInput{Command: "exit 42"})
	out, toolErr := tool.Execute(context.Background(), input)
	require.NoError(t, toolErr) // tool itself doesn't error; exit code is in output
	require.NotNil(t, out)

	var result map[string]interface{}
	require.NoError(t, json.Unmarshal(out, &result))
	assert.Equal(t, float64(42), result["exit_code"])
}

func TestBashTool_timeout(t *testing.T) {
	tool, err := builtin.NewBashTool()
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	input, _ := json.Marshal(builtin.BashInput{Command: "sleep 10"})
	out, toolErr := tool.Execute(ctx, input)
	require.NoError(t, toolErr)

	var result map[string]interface{}
	require.NoError(t, json.Unmarshal(out, &result))
	// Should have a non-zero exit code and error message
	assert.NotEqual(t, float64(0), result["exit_code"])
}

func TestBashTool_emptyCommand(t *testing.T) {
	tool, err := builtin.NewBashTool()
	require.NoError(t, err)

	input, _ := json.Marshal(builtin.BashInput{Command: ""})
	_, err = tool.Execute(context.Background(), input)
	assert.Error(t, err)
}

func TestBashTool_schema(t *testing.T) {
	tool, err := builtin.NewBashTool()
	require.NoError(t, err)
	assert.True(t, json.Valid(tool.Schema()))
}
