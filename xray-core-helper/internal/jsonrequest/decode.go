// Package jsonrequest decodes the shared command envelope. Field requirements
// and the meaning of null belong to the command's own validation.
package jsonrequest

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// Decode reads one JSON object, rejecting unknown fields and duplicate keys.
// The HTTP caller must bound the reader before invoking Decode.
func Decode(body io.Reader, target any) error {
	if body == nil {
		return io.EOF
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return errors.New("one JSON object required")
	}
	if err := validateUniqueJSON(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func validateUniqueJSON(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := uniqueJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing JSON value")
	}
	return nil
}

func uniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return errors.New("duplicate object key")
				}
				seen[key] = struct{}{}
				if err := uniqueJSONValue(decoder); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("object is incomplete")
			}
		case '[':
			for decoder.More() {
				if err := uniqueJSONValue(decoder); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("array is incomplete")
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	return nil
}
