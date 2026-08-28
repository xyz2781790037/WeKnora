package plugin

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strictTestSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "minLength": 2},
			"port": map[string]any{"type": "integer", "minimum": 1.0, "maximum": 65535.0},
		},
		"required": []any{"name"},
	}
}

func TestValidateConfigSchemaRejectsMalformedRequired(t *testing.T) {
	schema := strictTestSchema()
	schema["required"] = "name"
	require.ErrorContains(t, ValidateConfigSchema(schema), "required must be an array")

	schema["required"] = []any{"name", 42}
	require.ErrorContains(t, ValidateConfigSchema(schema), "only strings")
}

func TestValidateConfigSchemaRejectsMalformedConstraints(t *testing.T) {
	schema := strictTestSchema()
	schema["additionalProperties"] = "false"
	require.ErrorContains(t, ValidateConfigSchema(schema), "additionalProperties must be boolean")

	schema = strictTestSchema()
	schema["properties"].(map[string]any)["name"].(map[string]any)["minLength"] = 1.5
	require.ErrorContains(t, ValidateConfigSchema(schema), "non-negative integer")

	schema = strictTestSchema()
	schema["properties"].(map[string]any)["name"].(map[string]any)["minimum"] = 1
	require.ErrorContains(t, ValidateConfigSchema(schema), "require number or integer")
}

func TestValidateConfigValuesRejectsUnknownFieldWhenClosed(t *testing.T) {
	err := ValidateConfigValues(strictTestSchema(), map[string]any{"name": "ok", "unexpected": "secret"})
	require.ErrorContains(t, err, "unexpected")
}

func TestValidateConfigValuesAllowsUnknownFieldWhenOpen(t *testing.T) {
	schema := strictTestSchema()
	schema["additionalProperties"] = true
	require.NoError(t, ValidateConfigValues(schema, map[string]any{"name": "ok", "extension": true}))
}

func TestValidateConfigValuesAppliesPrimitiveConstraints(t *testing.T) {
	err := ValidateConfigValues(strictTestSchema(), map[string]any{"name": "x", "port": 70000})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name")

	err = ValidateConfigValues(strictTestSchema(), map[string]any{"name": "ok", "port": 70000})
	require.ErrorContains(t, err, "at most")
}
