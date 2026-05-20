package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/nareg/goagent/tools"
)

const bashTimeout = 30 * time.Second

// BashInput is the input schema for BashTool.
type BashInput struct {
	Command string `json:"command" description:"Shell command to execute"`
}

// BashTool executes shell commands via /bin/sh.
type BashTool struct{ schema json.RawMessage }

// NewBashTool constructs a BashTool, pre-generating its JSON Schema.
func NewBashTool() (*BashTool, error) {
	s, err := tools.GenerateSchema(BashInput{})
	if err != nil {
		return nil, fmt.Errorf("builtin.NewBashTool: %w", err)
	}
	return &BashTool{schema: s}, nil
}

func (t *BashTool) Name() string             { return "bash" }
func (t *BashTool) Description() string      { return "Execute a shell command and return stdout, stderr, and exit status." }
func (t *BashTool) Schema() json.RawMessage  { return t.schema }

func (t *BashTool) Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var in BashInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("bash.Execute: parse input: %w", err)
	}
	if in.Command == "" {
		return nil, fmt.Errorf("bash.Execute: command is required")
	}

	ctx, cancel := context.WithTimeout(ctx, bashTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", in.Command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	result := map[string]interface{}{
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"exit_code": 0,
	}
	if runErr != nil {
		if ctx.Err() != nil {
			result["exit_code"] = -1
			result["error"] = "command timed out or was cancelled"
		} else if exitErr, ok := runErr.(*exec.ExitError); ok {
			result["exit_code"] = exitErr.ExitCode()
		} else {
			result["exit_code"] = -1
			result["error"] = runErr.Error()
		}
	}

	out, _ := json.Marshal(result)
	return out, nil
}
