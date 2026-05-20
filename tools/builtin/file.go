package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nareg/goagent/tools"
)

// --- FileReadTool ---

// FileReadInput is the input schema for FileReadTool.
type FileReadInput struct {
	Path string `json:"path" description:"Path of the file to read"`
}

// FileReadTool reads the contents of a file.
type FileReadTool struct{ schema json.RawMessage }

// NewFileReadTool constructs a FileReadTool.
func NewFileReadTool() (*FileReadTool, error) {
	s, err := tools.GenerateSchema(FileReadInput{})
	if err != nil {
		return nil, fmt.Errorf("builtin.NewFileReadTool: %w", err)
	}
	return &FileReadTool{schema: s}, nil
}

func (t *FileReadTool) Name() string            { return "file_read" }
func (t *FileReadTool) Description() string     { return "Read the contents of a file from disk." }
func (t *FileReadTool) Schema() json.RawMessage { return t.schema }

func (t *FileReadTool) Execute(_ context.Context, input json.RawMessage) (json.RawMessage, error) {
	var in FileReadInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("file_read.Execute: %w", err)
	}
	if in.Path == "" {
		return nil, fmt.Errorf("file_read.Execute: path is required")
	}
	data, err := os.ReadFile(in.Path)
	if err != nil {
		return nil, fmt.Errorf("file_read.Execute: %w", err)
	}
	out, _ := json.Marshal(map[string]string{"content": string(data), "path": in.Path})
	return out, nil
}

// --- FileWriteTool ---

// FileWriteInput is the input schema for FileWriteTool.
type FileWriteInput struct {
	Path    string `json:"path"    description:"Path of the file to write (created if absent)"`
	Content string `json:"content" description:"Content to write to the file"`
}

// FileWriteTool writes content to a file, creating parent directories as needed.
type FileWriteTool struct{ schema json.RawMessage }

// NewFileWriteTool constructs a FileWriteTool.
func NewFileWriteTool() (*FileWriteTool, error) {
	s, err := tools.GenerateSchema(FileWriteInput{})
	if err != nil {
		return nil, fmt.Errorf("builtin.NewFileWriteTool: %w", err)
	}
	return &FileWriteTool{schema: s}, nil
}

func (t *FileWriteTool) Name() string            { return "file_write" }
func (t *FileWriteTool) Description() string     { return "Write content to a file, creating parent directories if needed." }
func (t *FileWriteTool) Schema() json.RawMessage { return t.schema }

func (t *FileWriteTool) Execute(_ context.Context, input json.RawMessage) (json.RawMessage, error) {
	var in FileWriteInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("file_write.Execute: %w", err)
	}
	if in.Path == "" {
		return nil, fmt.Errorf("file_write.Execute: path is required")
	}
	if err := os.MkdirAll(filepath.Dir(in.Path), 0755); err != nil {
		return nil, fmt.Errorf("file_write.Execute: create dirs: %w", err)
	}
	if err := os.WriteFile(in.Path, []byte(in.Content), 0644); err != nil {
		return nil, fmt.Errorf("file_write.Execute: %w", err)
	}
	out, _ := json.Marshal(map[string]string{"status": "ok", "path": in.Path})
	return out, nil
}
