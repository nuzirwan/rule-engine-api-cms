package flow

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// InputValidationError represents a single validation failure with field path
// and descriptive message. Multiple errors are collected and returned together.
type InputValidationError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// InputValidationErrors is a collection of validation errors.
type InputValidationErrors []InputValidationError

// Error implements error for InputValidationErrors.
func (e InputValidationErrors) Error() string {
	if len(e) == 0 {
		return "validation failed"
	}
	if len(e) == 1 {
		return fmt.Sprintf("validation failed: %s: %s", e[0].Field, e[0].Message)
	}
	return fmt.Sprintf("validation failed: %d errors", len(e))
}

// Unwrap returns ErrValidation so errors.Is(err, ErrValidation) works.
func (e InputValidationErrors) Unwrap() error {
	return ErrValidation
}

// ExtractInput extracts request data into a flat map based on the TriggerInput spec.
// Path params come from pathParams (populated by the router match).
// Query params are extracted for names listed in spec.Query.
// Headers are extracted for names listed in spec.Headers (case-insensitive).
// Body is parsed as JSON when spec.Body is true AND the request has a body.
func ExtractInput(r *http.Request, pathParams map[string]string, spec TriggerInput) (map[string]any, error) {
	input := make(map[string]any)

	// 1. Path params: always extract all provided path params.
	for name, val := range pathParams {
		input[name] = val
	}

	// 2. Query params: extract only names listed in spec.Query.
	if len(spec.Query) > 0 {
		query := r.URL.Query()
		for _, name := range spec.Query {
			if val := query.Get(name); val != "" {
				input[name] = val
			}
		}
	}

	// 3. Headers: extract only names listed in spec.Headers (case-insensitive).
	for _, name := range spec.Headers {
		if val := r.Header.Get(name); val != "" {
			// Use the declared name (not canonicalized) as the key.
			input[name] = val
		}
	}

	// 4. Body: parse JSON when spec.Body is true AND body exists.
	if spec.Body && r.Body != nil && r.ContentLength != 0 {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, newErr(ClassValidation, "failed to read request body")
		}
		if len(bodyBytes) > 0 {
			var bodyData any
			if err := json.Unmarshal(bodyBytes, &bodyData); err != nil {
				return nil, newErr(ClassValidation, "invalid JSON body")
			}
			// If body is an object, merge its fields into input under "body".
			// This allows the schema to validate body.fieldName.
			input["body"] = bodyData
		}
	}

	return input, nil
}

// ValidateInput validates the input map against a compiled JSON Schema.
// Returns nil if validation passes or schema is nil (no validation configured).
// Returns InputValidationErrors with field paths and messages on failure.
func ValidateInput(input map[string]any, schema *jsonschema.Schema) error {
	if schema == nil {
		return nil
	}

	err := schema.Validate(input)
	if err == nil {
		return nil
	}

	// Convert JSON Schema validation errors to our error format.
	var errs InputValidationErrors
	if ve, ok := err.(*jsonschema.ValidationError); ok {
		errs = extractValidationErrors(ve)
	} else {
		// Unexpected error type; wrap as a generic validation error.
		errs = InputValidationErrors{{
			Field:   "",
			Code:    "validation_error",
			Message: err.Error(),
		}}
	}

	if len(errs) == 0 {
		// Should not happen, but handle gracefully.
		errs = InputValidationErrors{{
			Field:   "",
			Code:    "validation_error",
			Message: err.Error(),
		}}
	}

	return errs
}

// extractValidationErrors recursively extracts errors from a ValidationError tree.
func extractValidationErrors(ve *jsonschema.ValidationError) InputValidationErrors {
	var errs InputValidationErrors

	// If this node has causes, recurse into them.
	if len(ve.Causes) > 0 {
		for _, cause := range ve.Causes {
			errs = append(errs, extractValidationErrors(cause)...)
		}
		return errs
	}

	// Leaf error: extract field path, code, and message.
	field := instancePathToField(ve.InstanceLocation)
	code := errorToCode(ve)
	msg := ve.Error()

	// Clean up the message: remove the "at '/path': " prefix if present.
	if idx := strings.Index(msg, ": "); idx != -1 && strings.HasPrefix(msg, "at '") {
		msg = msg[idx+2:]
	}

	errs = append(errs, InputValidationError{
		Field:   field,
		Code:    code,
		Message: msg,
	})
	return errs
}

// instancePathToField converts a JSON Pointer instance location to a dot-path field name.
// e.g., ["body", "user", "name"] -> "body.user.name", [] -> ""
func instancePathToField(loc []string) string {
	if len(loc) == 0 {
		return ""
	}
	return strings.Join(loc, ".")
}

// errorToCode extracts a short code from the ErrorKind.
func errorToCode(ve *jsonschema.ValidationError) string {
	switch ve.ErrorKind.(type) {
	case *kind.Required:
		return "required"
	case *kind.Type:
		return "type"
	case *kind.Pattern:
		return "pattern"
	case *kind.Enum:
		return "enum"
	case *kind.Minimum, *kind.ExclusiveMinimum:
		return "minimum"
	case *kind.Maximum, *kind.ExclusiveMaximum:
		return "maximum"
	case *kind.MinLength:
		return "min_length"
	case *kind.MaxLength:
		return "max_length"
	case *kind.MinItems:
		return "min_items"
	case *kind.MaxItems:
		return "max_items"
	case *kind.AdditionalProperties:
		return "additional_properties"
	case *kind.Format:
		return "format"
	case *kind.MinProperties:
		return "min_properties"
	case *kind.MaxProperties:
		return "max_properties"
	case *kind.UniqueItems:
		return "unique_items"
	case *kind.Const:
		return "const"
	case *kind.MultipleOf:
		return "multiple_of"
	default:
		return "validation_error"
	}
}

// CompileSchema compiles a JSON Schema document from raw JSON bytes.
// Returns nil schema and nil error when raw is empty (no schema configured).
// Returns a ClassValidation error when the schema is invalid.
func CompileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	// Parse the schema JSON into a generic structure.
	var schemaDoc any
	if err := json.Unmarshal(raw, &schemaDoc); err != nil {
		return nil, newErr(ClassValidation, "invalid schema JSON: "+err.Error())
	}

	// Compile the schema using jsonschema/v6.
	c := jsonschema.NewCompiler()
	if err := c.AddResource("schema.json", schemaDoc); err != nil {
		return nil, newErr(ClassValidation, "invalid schema: "+err.Error())
	}

	schema, err := c.Compile("schema.json")
	if err != nil {
		return nil, newErr(ClassValidation, "schema compilation failed: "+err.Error())
	}

	return schema, nil
}

// ExtractAndValidate combines extraction and validation in one call.
// When compiled is nil (no schema configured), validation is skipped.
// Returns the extracted input map and any extraction or validation error.
func ExtractAndValidate(r *http.Request, pathParams map[string]string, spec TriggerInput, compiled *jsonschema.Schema) (map[string]any, error) {
	input, err := ExtractInput(r, pathParams, spec)
	if err != nil {
		return nil, err
	}

	if err := ValidateInput(input, compiled); err != nil {
		return nil, err
	}

	return input, nil
}
