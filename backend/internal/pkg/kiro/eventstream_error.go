package kiro

import "fmt"

// EventStreamError reports a modeled or unmodeled AWS error. Payload messages
// are deliberately omitted because they can echo request contents.
type EventStreamError struct {
	MessageType string
	Code        string
}

func (e *EventStreamError) Error() string {
	return fmt.Sprintf("kiro upstream eventstream %s: %s", e.MessageType, e.Code)
}

func safeEventStreamErrorCode(code string) string {
	if len(code) == 0 || len(code) > 128 {
		return "unknown"
	}
	for _, c := range code {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '_' || c == '-' || c == '.' || c == '#' || c == ':' {
			continue
		}
		return "unknown"
	}
	return code
}
