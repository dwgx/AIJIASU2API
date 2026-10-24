package webhook

import "regexp"

// Redacted replaces every value whose key looks sensitive.
const Redacted = "[REDACTED]"

// sensitiveKey matches field names that must never leave the process. The
// match is deliberately broad: a false positive hides a harmless field, while
// a false negative leaks a credential.
var sensitiveKey = regexp.MustCompile(`(?i)(secret|token|password|authorization|cookie|machinecode|identity)`)

// Scrub returns a deep copy of data with every sensitive value replaced by
// Redacted. Map keys are matched case-insensitively; nested maps and slices are
// scrubbed recursively. The caller's map is never modified.
func Scrub(data map[string]any) map[string]any {
	if data == nil {
		return nil
	}
	out := make(map[string]any, len(data))
	for key, value := range data {
		out[key] = scrubValue(key, value)
	}
	return out
}

func scrubValue(key string, value any) any {
	if sensitiveKey.MatchString(key) {
		return Redacted
	}
	switch typed := value.(type) {
	case map[string]any:
		return Scrub(typed)
	case map[string]string:
		out := make(map[string]string, len(typed))
		for nestedKey, nestedValue := range typed {
			if sensitiveKey.MatchString(nestedKey) {
				out[nestedKey] = Redacted
				continue
			}
			out[nestedKey] = nestedValue
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = scrubValue("", item)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(typed))
		for i, item := range typed {
			out[i] = Scrub(item)
		}
		return out
	}
	return value
}
