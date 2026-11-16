package service

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// upstreamCreditsRequestHeader lets a downstream gateway opt in to receiving
	// the upstream credits a Kiro request consumed. Without it the response
	// stays unchanged and the internal credits field is stripped.
	upstreamCreditsRequestHeader = "X-Sub2API-Upstream-Credits"
	// upstreamCreditsUsageField is the public usage field the credits are
	// exposed under on Anthropic Messages and OpenAI Responses usage when opted in.
	upstreamCreditsUsageField = "upstream_credits"
)

// upstreamCreditsRequested reports whether the caller asked for the upstream
// credits to be included in the response usage block.
func upstreamCreditsRequested(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(c.GetHeader(upstreamCreditsRequestHeader))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// withResponsesUpstreamCredits adds the Kiro credits a request consumed to the
// usage object at usagePath of an OpenAI Responses payload ("usage" for a
// response body, "response.usage" for a terminal stream event). The payload is
// returned unchanged unless the caller opted in and credits were reported, so
// requests served by non-Kiro accounts never carry the field.
func withResponsesUpstreamCredits(c *gin.Context, payload []byte, usagePath string, credits float64) []byte {
	if credits <= 0 || !upstreamCreditsRequested(c) || !gjson.GetBytes(payload, usagePath).IsObject() {
		return payload
	}
	next, err := sjson.SetBytes(payload, usagePath+"."+upstreamCreditsUsageField, credits)
	if err != nil {
		return payload
	}
	return next
}

// isResponsesTerminalEvent reports whether a Responses stream event carries the
// final response, and with it the final usage.
func isResponsesTerminalEvent(eventType string) bool {
	switch eventType {
	case "response.completed", "response.incomplete", "response.failed":
		return true
	}
	return false
}

func kiroCreditsFromUsageGJSON(usage gjson.Result) float64 {
	if !usage.Exists() {
		return 0
	}
	for _, key := range []string{"_sub2api_kiro_credits", "kiro_credits", "kiroCredits", "credits", "creditsUsed", "creditUsage"} {
		if v := usage.Get(key); v.Exists() && v.Float() > 0 {
			return v.Float()
		}
	}
	return 0
}
