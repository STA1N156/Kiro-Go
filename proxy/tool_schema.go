package proxy

import (
	"encoding/json"
	"strings"
)

const maxKiroToolSpecBytes = 31 * 1024

var multimodalUnsupportedSchemaKeys = map[string]bool{
	"anyOf":                 true,
	"oneOf":                 true,
	"allOf":                 true,
	"contains":              true,
	"dependentSchemas":      true,
	"patternProperties":     true,
	"$defs":                 true,
	"definitions":           true,
	"prefixItems":           true,
	"unevaluatedProperties": true,
}

func prepareKiroPayload(payload *KiroPayload) {
	if payload == nil {
		return
	}
	hasImages := processPayloadImages(payload)
	normalizeMessageTools(&payload.ConversationState.CurrentMessage.UserInputMessage, hasImages)
	for i := range payload.ConversationState.History {
		normalizeMessageTools(payload.ConversationState.History[i].UserInputMessage, hasImages)
	}
}

func normalizeMessageTools(message *KiroUserInputMessage, hasImages bool) {
	if message == nil || message.UserInputMessageContext == nil {
		return
	}
	tools := message.UserInputMessageContext.Tools
	for i := range tools {
		spec := &tools[i].ToolSpecification
		original := spec.InputSchema.JSON
		if hasImages && schemaContainsUnsupportedMultimodalKeyword(original) {
			spec.InputSchema.JSON = permissiveObjectSchema()
		} else {
			spec.InputSchema.JSON = normalizeKiroToolSchema(original)
		}
		if toolSpecSize(tools[i]) > maxKiroToolSpecBytes {
			spec.Description = "[parameter schema omitted because it exceeded the upstream size limit; infer the minimal arguments from the tool name and conversation]"
			spec.InputSchema.JSON = map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}
		}
	}
}

func permissiveObjectSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":                 "object",
		"properties":           map[string]interface{}{},
		"required":             []interface{}{},
		"additionalProperties": true,
	}
}

func normalizeKiroToolSchema(raw interface{}) interface{} {
	root, ok := raw.(map[string]interface{})
	if !ok {
		return permissiveObjectSchema()
	}
	cleaned := cleanSchemaValue(root, root, 0)
	obj, ok := cleaned.(map[string]interface{})
	if !ok {
		return permissiveObjectSchema()
	}
	if typ, _ := obj["type"].(string); strings.TrimSpace(typ) == "" {
		obj["type"] = "object"
	}
	if _, ok := obj["properties"].(map[string]interface{}); !ok {
		obj["properties"] = map[string]interface{}{}
	}
	required := make([]interface{}, 0)
	if values, ok := obj["required"].([]interface{}); ok {
		for _, value := range values {
			if name, ok := value.(string); ok && name != "" {
				required = append(required, name)
			}
		}
	}
	obj["required"] = required
	switch obj["additionalProperties"].(type) {
	case bool, map[string]interface{}:
	default:
		obj["additionalProperties"] = true
	}
	return obj
}

func cleanSchemaValue(node interface{}, root map[string]interface{}, depth int) interface{} {
	if depth > 16 {
		return permissiveObjectSchema()
	}
	obj, ok := node.(map[string]interface{})
	if !ok {
		if values, ok := node.([]interface{}); ok {
			out := make([]interface{}, 0, len(values))
			for _, value := range values {
				out = append(out, cleanSchemaValue(value, root, depth+1))
			}
			return out
		}
		return node
	}

	if ref, ok := obj["$ref"].(string); ok {
		if target := resolveLocalSchemaRef(ref, root); target != nil {
			return cleanSchemaValue(target, root, depth+1)
		}
		return permissiveObjectSchema()
	}

	for _, unionKey := range []string{"anyOf", "oneOf"} {
		if variants, ok := obj[unionKey].([]interface{}); ok {
			var selected interface{}
			for _, variant := range variants {
				if _, ok := variant.(map[string]interface{}); ok {
					selected = variant
					break
				}
			}
			if selected == nil {
				selected = permissiveObjectSchema()
			}
			cleaned := cleanSchemaValue(selected, root, depth+1)
			if cleanedObj, ok := cleaned.(map[string]interface{}); ok {
				for _, key := range []string{"description", "title", "default"} {
					if _, exists := cleanedObj[key]; !exists {
						if value, exists := obj[key]; exists {
							cleanedObj[key] = value
						}
					}
				}
			}
			return cleaned
		}
	}

	if variants, ok := obj["allOf"].([]interface{}); ok {
		merged := make(map[string]interface{})
		for _, variant := range variants {
			cleaned, ok := cleanSchemaValue(variant, root, depth+1).(map[string]interface{})
			if !ok {
				continue
			}
			mergeSchemaMaps(merged, cleaned)
		}
		for _, key := range []string{"description", "title", "default"} {
			if _, exists := merged[key]; !exists {
				if value, exists := obj[key]; exists {
					merged[key] = value
				}
			}
		}
		return merged
	}

	out := make(map[string]interface{}, len(obj))
	for key, value := range obj {
		if key == "$defs" || key == "definitions" {
			continue
		}
		out[key] = cleanSchemaValue(value, root, depth+1)
	}
	return out
}

func mergeSchemaMaps(dst, src map[string]interface{}) {
	for key, value := range src {
		switch key {
		case "properties":
			target, _ := dst[key].(map[string]interface{})
			if target == nil {
				target = map[string]interface{}{}
				dst[key] = target
			}
			if incoming, ok := value.(map[string]interface{}); ok {
				for name, schema := range incoming {
					target[name] = schema
				}
			}
		case "required":
			existing, _ := dst[key].([]interface{})
			incoming, _ := value.([]interface{})
			seen := map[string]bool{}
			for _, item := range existing {
				if name, ok := item.(string); ok {
					seen[name] = true
				}
			}
			for _, item := range incoming {
				name, ok := item.(string)
				if ok && !seen[name] {
					existing = append(existing, name)
					seen[name] = true
				}
			}
			dst[key] = existing
		default:
			dst[key] = value
		}
	}
}

func resolveLocalSchemaRef(ref string, root map[string]interface{}) interface{} {
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	var current interface{} = root
	for _, segment := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
		obj, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		current, ok = obj[segment]
		if !ok {
			return nil
		}
	}
	return current
}

func schemaContainsUnsupportedMultimodalKeyword(value interface{}) bool {
	switch node := value.(type) {
	case map[string]interface{}:
		for key, child := range node {
			if multimodalUnsupportedSchemaKeys[key] || schemaContainsUnsupportedMultimodalKeyword(child) {
				return true
			}
		}
	case []interface{}:
		for _, child := range node {
			if schemaContainsUnsupportedMultimodalKeyword(child) {
				return true
			}
		}
	}
	return false
}

func toolSpecSize(tool KiroToolWrapper) int {
	encoded, err := json.Marshal(tool)
	if err != nil {
		return 0
	}
	return len(encoded)
}

func arrayObjectSchemaFields(raw interface{}) map[string]bool {
	schema, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	properties, _ := schema["properties"].(map[string]interface{})
	if len(properties) == 0 {
		return nil
	}
	fields := make(map[string]bool)
	for name, value := range properties {
		property, _ := value.(map[string]interface{})
		kind, _ := property["type"].(string)
		if kind == "array" || kind == "object" {
			fields[name] = true
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func repairToolInput(input map[string]interface{}, fields map[string]bool) {
	for name := range fields {
		encoded, ok := input[name].(string)
		if !ok || encoded == "" {
			continue
		}
		var value interface{}
		if err := json.Unmarshal([]byte(encoded), &value); err != nil {
			continue
		}
		switch value.(type) {
		case []interface{}, map[string]interface{}:
			input[name] = value
		default:
			// Leave normal string values untouched.
		}
	}
}
