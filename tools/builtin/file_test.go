package builtin_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nareg/goagent/tools/builtin"
)

func TestFileWriteRead_roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "hello, agent"

	writer, err := builtin.NewFileWriteTool()
	require.NoError(t, err)
	assert.Equal(t, "file_write", writer.Name())

	input, _ := json.Marshal(builtin.FileWriteInput{Path: path, Content: content})
	out, err := writer.Execute(context.Background(), input)
	require.NoError(t, err)
	var writeResult map[string]string
	require.NoError(t, json.Unmarshal(out, &writeResult))
	assert.Equal(t, "ok", writeResult["status"])

	reader, err := builtin.NewFileReadTool()
	require.NoError(t, err)
	assert.Equal(t, "file_read", reader.Name())

	input, _ = json.Marshal(builtin.FileReadInput{Path: path})
	out, err = reader.Execute(context.Background(), input)
	require.NoError(t, err)
	var readResult map[string]string
	require.NoError(t, json.Unmarshal(out, &readResult))
	assert.Equal(t, content, readResult["content"])
}

func TestFileWriteTool_createsParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "c", "file.txt")

	writer, err := builtin.NewFileWriteTool()
	require.NoError(t, err)

	input, _ := json.Marshal(builtin.FileWriteInput{Path: path, Content: "deep"})
	_, err = writer.Execute(context.Background(), input)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "deep", string(data))
}

func TestFileReadTool_missingFile(t *testing.T) {
	reader, err := builtin.NewFileReadTool()
	require.NoError(t, err)

	input, _ := json.Marshal(builtin.FileReadInput{Path: "/nonexistent/path/file.txt"})
	_, err = reader.Execute(context.Background(), input)
	assert.Error(t, err)
}

func TestFileReadTool_emptyPath(t *testing.T) {
	reader, err := builtin.NewFileReadTool()
	require.NoError(t, err)

	input, _ := json.Marshal(builtin.FileReadInput{Path: ""})
	_, err = reader.Execute(context.Background(), input)
	assert.Error(t, err)
}

func TestFileTools_schemas(t *testing.T) {
	reader, _ := builtin.NewFileReadTool()
	writer, _ := builtin.NewFileWriteTool()
	assert.True(t, json.Valid(reader.Schema()))
	assert.True(t, json.Valid(writer.Schema()))
}
