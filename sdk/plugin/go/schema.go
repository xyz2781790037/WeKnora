package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

var supportedConfigTypes = map[string]struct{}{
	"string": {}, "number": {}, "integer": {}, "boolean": {}, "array": {},
}

// ValidateConfigSchema validates the intentionally small JSON Schema subset
// rendered by WeKnora's shared plugin form. Keeping this subset explicit makes
// a manifest behave identically in the browser and in backend validation.
func ValidateConfigSchema(schema map[string]any) error {
	if schema == nil {
		return errors.New("config.schema is required")
	}
	rootType, ok := schema["type"].(string)
	if !ok || strings.TrimSpace(rootType) != "object" {
		return errors.New("config.schema.type must be object")
	}
	if value, exists := schema["additionalProperties"]; exists {
		if _, ok := value.(bool); !ok {
			return errors.New("config.schema.additionalProperties must be boolean")
		}
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return errors.New("config.schema.properties must be an object")
	}
	for key, raw := range properties {
		key = strings.TrimSpace(key)
		if key == "" || strings.Contains(key, ".") {
			return fmt.Errorf("config.schema property %q must be a non-empty top-level field", key)
		}
		property, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("config.schema property %q must be an object", key)
		}
		propertyType, ok := property["type"].(string)
		if !ok {
			return fmt.Errorf("config.schema property %q type must be a string", key)
		}
		propertyType = strings.TrimSpace(propertyType)
		if _, supported := supportedConfigTypes[propertyType]; !supported {
			return fmt.Errorf("config.schema property %q uses unsupported type %q", key, propertyType)
		}
		if propertyType == "array" {
			items, ok := property["items"].(map[string]any)
			if !ok || stringSchemaValue(items["type"]) != "string" {
				return fmt.Errorf("config.schema array property %q must declare string items", key)
			}
		}
		if values, exists := property["enum"]; exists {
			enumValues, ok := values.([]any)
			if !ok || len(enumValues) == 0 {
				return fmt.Errorf("config.schema property %q enum must be a non-empty array", key)
			}
			for _, value := range enumValues {
				if !configValueMatchesType(propertyType, value) {
					return fmt.Errorf("config.schema property %q enum value does not match type %s", key, propertyType)
				}
			}
		}
		if value, exists := property["default"]; exists && !configValueMatchesType(propertyType, value) {
			return fmt.Errorf("config.schema property %q default does not match type %s", key, propertyType)
		}
		if err := validateNumericBounds(key, propertyType, property); err != nil {
			return err
		}
	}
	required, err := schemaRequiredFields(schema["required"])
	if err != nil {
		return err
	}
	seenRequired := make(map[string]struct{}, len(required))
	for _, key := range required {
		if strings.TrimSpace(key) == "" {
			return errors.New("config.schema.required cannot contain an empty field")
		}
		if _, duplicate := seenRequired[key]; duplicate {
			return fmt.Errorf("config.schema.required contains duplicate field %q", key)
		}
		seenRequired[key] = struct{}{}
		if _, exists := properties[key]; !exists {
			return fmt.Errorf("config.schema required field %q is not defined in properties", key)
		}
	}
	return nil
}

// ValidateConfigValues validates one settings+credentials object before it is
// sent to a plugin. Plugins still run their own semantic ValidateConfig hook;
// this host-side check handles required fields and primitive constraints.
func ValidateConfigValues(schema map[string]any, values map[string]any) error {
	if err := ValidateConfigSchema(schema); err != nil {
		return err
	}
	values = NormalizeConfigValues(schema, values)
	properties := schema["properties"].(map[string]any)
	required, _ := schemaRequiredFields(schema["required"])
	for _, key := range required {
		value, exists := values[key]
		if !exists || emptyConfigValue(value) {
			return fmt.Errorf("config field %q is required", key)
		}
	}
	for key, value := range values {
		raw, exists := properties[key]
		if !exists {
			if additional, _ := schema["additionalProperties"].(bool); !additional {
				return fmt.Errorf("config field %q is not declared in config.schema.properties", key)
			}
			continue
		}
		if value == nil {
			continue
		}
		property := raw.(map[string]any)
		propertyType := stringSchemaValue(property["type"])
		if !runtimeConfigValueMatchesType(propertyType, value) {
			return fmt.Errorf("config field %q must be %s", key, propertyType)
		}
		if values, exists := property["enum"].([]any); exists && !containsRuntimeSchemaValue(values, value) {
			return fmt.Errorf("config field %q is not one of the allowed values", key)
		}
		if stringValue, ok := value.(string); ok {
			if minimum, ok := schemaNumber(property["minLength"]); ok && len([]rune(stringValue)) < int(minimum) {
				return fmt.Errorf("config field %q is shorter than %d characters", key, int(minimum))
			}
		}
		if number, ok := runtimeSchemaNumber(value); ok {
			if minimum, exists := schemaNumber(property["minimum"]); exists && number < minimum {
				return fmt.Errorf("config field %q must be at least %v", key, minimum)
			}
			if maximum, exists := schemaNumber(property["maximum"]); exists && number > maximum {
				return fmt.Errorf("config field %q must be at most %v", key, maximum)
			}
		}
	}
	return nil
}

// NormalizeConfigValues restores non-string JSON values from feature stores
// that persist extension settings as string maps. String fields are never
// decoded, so values such as numeric-looking model names remain unchanged.
func NormalizeConfigValues(schema map[string]any, values map[string]any) map[string]any {
	normalized := make(map[string]any, len(values))
	properties, _ := schema["properties"].(map[string]any)
	for key, value := range values {
		normalized[key] = value
		property, _ := properties[key].(map[string]any)
		propertyType := stringSchemaValue(property["type"])
		text, isString := value.(string)
		if !isString || propertyType == "" || propertyType == "string" {
			continue
		}
		var decoded any
		if json.Unmarshal([]byte(text), &decoded) == nil && configValueMatchesType(propertyType, decoded) {
			normalized[key] = decoded
		}
	}
	return normalized
}

func validateNumericBounds(key, propertyType string, property map[string]any) error {
	minimum, hasMinimum, err := schemaConstraintNumber(key, "minimum", property)
	if err != nil {
		return err
	}
	maximum, hasMaximum, err := schemaConstraintNumber(key, "maximum", property)
	if err != nil {
		return err
	}
	if (hasMinimum || hasMaximum) && propertyType != "number" && propertyType != "integer" {
		return fmt.Errorf("config.schema property %q numeric bounds require number or integer type", key)
	}
	if hasMinimum && hasMaximum && minimum > maximum {
		return fmt.Errorf("config.schema property %q minimum cannot exceed maximum", key)
	}
	minLength, hasMinLength, err := schemaConstraintNumber(key, "minLength", property)
	if err != nil {
		return err
	}
	if hasMinLength {
		if propertyType != "string" {
			return fmt.Errorf("config.schema property %q minLength requires string type", key)
		}
		if minLength < 0 || math.Trunc(minLength) != minLength {
			return fmt.Errorf("config.schema property %q minLength must be a non-negative integer", key)
		}
	}
	return nil
}

func schemaConstraintNumber(key, constraint string, property map[string]any) (float64, bool, error) {
	value, exists := property[constraint]
	if !exists {
		return 0, false, nil
	}
	number, ok := schemaNumber(value)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false, fmt.Errorf("config.schema property %q %s must be a finite number", key, constraint)
	}
	return number, true, nil
}

func configValueMatchesType(expected string, value any) bool {
	switch expected {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := schemaNumber(value)
		return ok
	case "integer":
		number, ok := schemaNumber(value)
		return ok && math.Trunc(number) == number
	case "array":
		valueOf := reflect.ValueOf(value)
		if valueOf.Kind() != reflect.Array && valueOf.Kind() != reflect.Slice {
			return false
		}
		for index := 0; index < valueOf.Len(); index++ {
			if _, ok := valueOf.Index(index).Interface().(string); !ok {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func runtimeConfigValueMatchesType(expected string, value any) bool {
	if configValueMatchesType(expected, value) {
		return true
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	switch expected {
	case "number":
		_, err := strconv.ParseFloat(text, 64)
		return err == nil
	case "integer":
		_, err := strconv.ParseInt(text, 10, 64)
		return err == nil
	case "boolean":
		_, err := strconv.ParseBool(text)
		return err == nil
	default:
		return false
	}
}

func schemaNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case float64:
		return number, true
	case float32:
		return float64(number), true
	default:
		return 0, false
	}
}

func runtimeSchemaNumber(value any) (float64, bool) {
	if number, ok := schemaNumber(value); ok {
		return number, true
	}
	text, ok := value.(string)
	if !ok {
		return 0, false
	}
	number, err := strconv.ParseFloat(text, 64)
	return number, err == nil
}

func schemaRequiredFields(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	raw, ok := value.([]any)
	if !ok {
		if typed, typedOK := value.([]string); typedOK {
			return append([]string(nil), typed...), nil
		}
		return nil, errors.New("config.schema.required must be an array of strings")
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			return nil, errors.New("config.schema.required must contain only strings")
		}
		result = append(result, text)
	}
	return result, nil
}

func stringSchemaValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func emptyConfigValue(value any) bool {
	if value == nil {
		return true
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) == ""
	case []string:
		return len(typed) == 0
	case []any:
		return len(typed) == 0
	default:
		return false
	}
}

func containsSchemaValue(values []any, target any) bool {
	for _, value := range values {
		if reflect.DeepEqual(value, target) {
			return true
		}
	}
	return false
}

func containsRuntimeSchemaValue(values []any, target any) bool {
	if containsSchemaValue(values, target) {
		return true
	}
	text, ok := target.(string)
	if !ok {
		return false
	}
	for _, value := range values {
		if fmt.Sprint(value) == text {
			return true
		}
	}
	return false
}
