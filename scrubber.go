package g8scope

import "strings"

// Scrubber performs client-side PII scrubbing: it redacts configured keys
// (case-insensitive) anywhere in a nested context, replacing the value with
// "[REDACTED]".
type Scrubber struct {
	keys map[string]struct{}
}

// Redacted is the placeholder substituted for scrubbed values.
const Redacted = "[REDACTED]"

// NewScrubber builds a Scrubber for the given (case-insensitive) keys.
func NewScrubber(keys []string) *Scrubber {
	set := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		set[strings.ToLower(k)] = struct{}{}
	}
	return &Scrubber{keys: set}
}

// Scrub returns a redacted deep copy of the supplied context map. The input is
// never mutated.
func (s *Scrubber) Scrub(data map[string]interface{}) map[string]interface{} {
	if data == nil {
		return nil
	}
	out := make(map[string]interface{}, len(data))
	for k, v := range data {
		if _, hit := s.keys[strings.ToLower(k)]; hit {
			out[k] = Redacted
			continue
		}
		out[k] = s.walk(v)
	}
	return out
}

func (s *Scrubber) walk(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for k, val := range v {
			if _, hit := s.keys[strings.ToLower(k)]; hit {
				out[k] = Redacted
				continue
			}
			out[k] = s.walk(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, val := range v {
			out[i] = s.walk(val)
		}
		return out
	default:
		return value
	}
}
