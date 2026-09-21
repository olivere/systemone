package systemone

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
)

// Content is an immutable JSON string, object, or array. Its zero value is absent.
// Construct raw JSON with NewContent(jsontext.Value(data)); an ordinary string
// is encoded as a JSON string, not parsed as JSON.
type Content struct{ encoded string }

// NewContent validates and snapshots value. Strings must not be blank.
func NewContent(value any) (Content, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return Content{}, fmt.Errorf("systemone/content: %w: %w", ErrInvalidQuestion, err)
	}
	data = bytes.TrimSpace(data)
	if !jsontext.Value(data).IsValid() || len(data) == 0 {
		return Content{}, fmt.Errorf("systemone/content: %w: invalid JSON", ErrInvalidQuestion)
	}
	switch data[0] {
	case '"':
		var value string
		if err := json.Unmarshal(data, &value); err != nil || !nonempty(value) {
			return Content{}, fmt.Errorf("systemone/content: %w: string must be nonempty UTF-8", ErrInvalidQuestion)
		}
	case '{', '[':
	default:
		return Content{}, fmt.Errorf("systemone/content: %w: expected string, object, or array", ErrInvalidQuestion)
	}
	return Content{encoded: string(data)}, nil
}

// MustContent panics on invalid static content.
func MustContent(value any) Content {
	c, err := NewContent(value)
	if err != nil {
		panic(err)
	}
	return c
}

func (c Content) IsZero() bool { return c.encoded == "" }

// JSON returns an independent copy, or nil for absent content.
func (c Content) JSON() jsontext.Value {
	if c.IsZero() {
		return nil
	}
	return jsontext.Value(c.encoded)
}

// MarshalJSON rejects absent content rather than silently encoding null.
func (c Content) MarshalJSON() ([]byte, error) {
	if c.IsZero() {
		return nil, fmt.Errorf("systemone/content: %w: absent content", ErrInvalidQuestion)
	}
	return []byte(c.encoded), nil
}
