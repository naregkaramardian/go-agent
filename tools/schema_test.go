package tools_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nareg/goagent/tools"
)

type simpleInput struct {
	Query string `json:"query" description:"Search query"`
	Limit int    `json:"limit" description:"Max results" optional:"true"`
}

type nestedInput struct {
	Name   string      `json:"name" description:"Name"`
	Config simpleInput `json:"config" description:"Nested config"`
}

type enumInput struct {
	Mode string `json:"mode" description:"Operating mode" enum:"fast,slow,auto"`
}

type sliceInput struct {
	Tags []string `json:"tags" description:"List of tags"`
}

func TestGenerateSchema_basicTypes(t *testing.T) {
	raw, err := tools.GenerateSchema(simpleInput{})
	require.NoError(t, err)

	var schema map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &schema))

	assert.Equal(t, "object", schema["type"])

	props := schema["properties"].(map[string]interface{})
	query := props["query"].(map[string]interface{})
	assert.Equal(t, "string", query["type"])
	assert.Equal(t, "Search query", query["description"])

	// Required should contain query but not limit (optional:"true")
	required := schema["required"].([]interface{})
	assert.Contains(t, required, "query")
	assert.NotContains(t, required, "limit")
}

func TestGenerateSchema_enum(t *testing.T) {
	raw, err := tools.GenerateSchema(enumInput{})
	require.NoError(t, err)

	var schema map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &schema))

	props := schema["properties"].(map[string]interface{})
	mode := props["mode"].(map[string]interface{})
	enum := mode["enum"].([]interface{})
	assert.Equal(t, []interface{}{"fast", "slow", "auto"}, enum)
}

func TestGenerateSchema_slice(t *testing.T) {
	raw, err := tools.GenerateSchema(sliceInput{})
	require.NoError(t, err)

	var schema map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &schema))

	props := schema["properties"].(map[string]interface{})
	tags := props["tags"].(map[string]interface{})
	assert.Equal(t, "array", tags["type"])
	items := tags["items"].(map[string]interface{})
	assert.Equal(t, "string", items["type"])
}

func TestGenerateSchema_nested(t *testing.T) {
	raw, err := tools.GenerateSchema(nestedInput{})
	require.NoError(t, err)

	var schema map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &schema))

	props := schema["properties"].(map[string]interface{})
	cfg := props["config"].(map[string]interface{})
	assert.Equal(t, "object", cfg["type"])
	assert.NotNil(t, cfg["properties"])
}

func TestGenerateSchema_nonStruct(t *testing.T) {
	_, err := tools.GenerateSchema("not a struct")
	assert.Error(t, err)
}

func TestGenerateSchema_nil(t *testing.T) {
	_, err := tools.GenerateSchema(nil)
	assert.Error(t, err)
}

func TestGenerateSchema_validJSON(t *testing.T) {
	raw, err := tools.GenerateSchema(simpleInput{})
	require.NoError(t, err)
	assert.True(t, json.Valid(raw), "schema must be valid JSON")
}
