package webhook

import "regexp"

// Redacted replaces every value whose key looks sensitive, and the value of a
// credential-shaped assignment embedded in a string.
const Redacted = "[REDACTED]"

// sensitiveKey matches field names that must never leave the process. The
// match is deliberately broad: a false positive hides a harmless field, while
// a false negative leaks a credential.
var sensitiveKey = regexp.MustCompile(`(?i)(passwd|password|secret|token|api[_-]?key|credential|private[_-]?key|session|bearer|authorization|cookie|machinecode|identity)`)

// sensitiveValue matches a credential assignment embedded in a free-form
// string, such as a URL query ("...&token=abc") or a log-ish message
// ("password=hunter2"). Group 1 is the key label, group 2 the separator, and
// group 3 the value. A quoted value is consumed whole; otherwise the value
// stops at whitespace or a query/list separator so the rest of a URL survives.
// Only the value is hidden: the label stays readable so an operator can still
// see what was scrubbed.
var sensitiveValue = regexp.MustCompile(`(?i)\b(passwd|password|secret|token|api[_-]?key)(\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s&,;]+)`)

// Scrub returns a deep copy of data with every sensitive value replaced by
// Redacted. Map keys are matched case-insensitively; nested maps and slices are
// scrubbed recursively. String values are additionally scanned for embedded
// `key=value` credentials. The caller's map is never modified.
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
	case string:
		return scrubString(typed)
	case map[string]any:
		return Scrub(typed)
	case map[string]string:
		out := make(map[string]string, len(typed))
		for nestedKey, nestedValue := range typed {
			if sensitiveKey.MatchString(nestedKey) {
				out[nestedKey] = Redacted
				continue
			}
			out[nestedKey] = scrubString(nestedValue)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = scrubValue("", item)
		}
		return out
	case []string:
		out := make([]string, len(typed))
		for i, item := range typed {
			out[i] = scrubString(item)
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

// scrubString redacts credential assignments inside one string value.
func scrubString(value string) string {
	if !sensitiveValue.MatchString(value) {
		return value
	}
	return sensitiveValue.ReplaceAllString(value, "${1}${2}"+Redacted)
}
