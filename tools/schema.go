package tools

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// GenerateSchema produces a JSON Schema (draft-07 compatible) for a struct
// type, using `json` and `description` struct tags. Fields tagged
// `optional:"true"` are omitted from the required array.
func GenerateSchema(v interface{}) (json.RawMessage, error) {
	t := reflect.TypeOf(v)
	if t == nil {
		return nil, fmt.Errorf("tools.GenerateSchema: nil value")
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("tools.GenerateSchema: expected struct, got %s", t.Kind())
	}
	schema := buildObjectSchema(t)
	return json.Marshal(schema)
}

type jsonSchema struct {
	Type                 string                `json:"type"`
	Properties           map[string]jsonSchema `json:"properties,omitempty"`
	Required             []string              `json:"required,omitempty"`
	Description          string                `json:"description,omitempty"`
	Items                *jsonSchema           `json:"items,omitempty"`
	Enum                 []interface{}         `json:"enum,omitempty"`
	AdditionalProperties *jsonSchema           `json:"additionalProperties,omitempty"`
}

func buildObjectSchema(t reflect.Type) jsonSchema {
	schema := jsonSchema{
		Type:       "object",
		Properties: make(map[string]jsonSchema),
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := jsonFieldName(f)
		if name == "-" {
			continue
		}
		schema.Properties[name] = fieldSchema(f.Type, f)
		if f.Tag.Get("optional") != "true" {
			schema.Required = append(schema.Required, name)
		}
	}
	if len(schema.Required) == 0 {
		schema.Required = nil
	}
	return schema
}

func fieldSchema(t reflect.Type, f reflect.StructField) jsonSchema {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	desc := ""
	if f.Tag != "" {
		desc = f.Tag.Get("description")
	}
	s := jsonSchema{Description: desc}

	switch t.Kind() {
	case reflect.String:
		s.Type = "string"
		if tag := f.Tag.Get("enum"); tag != "" {
			for _, v := range strings.Split(tag, ",") {
				s.Enum = append(s.Enum, strings.TrimSpace(v))
			}
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		s.Type = "integer"
	case reflect.Float32, reflect.Float64:
		s.Type = "number"
	case reflect.Bool:
		s.Type = "boolean"
	case reflect.Slice:
		s.Type = "array"
		elem := fieldSchema(t.Elem(), reflect.StructField{})
		s.Items = &elem
	case reflect.Map:
		s.Type = "object"
		if t.Elem().Kind() != reflect.Interface {
			valSchema := fieldSchema(t.Elem(), reflect.StructField{})
			s.AdditionalProperties = &valSchema
		}
	case reflect.Struct:
		inner := buildObjectSchema(t)
		s.Type = inner.Type
		s.Properties = inner.Properties
		s.Required = inner.Required
	default:
		s.Type = "string"
	}
	return s
}

func jsonFieldName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" {
		return strings.ToLower(f.Name)
	}
	name := strings.Split(tag, ",")[0]
	if name == "" {
		return strings.ToLower(f.Name)
	}
	return name
}
