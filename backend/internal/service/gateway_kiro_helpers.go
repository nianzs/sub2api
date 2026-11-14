package service

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	// upstreamCreditsRequestHeader lets a downstream gateway opt in to receiving
	// the upstream credits a Kiro request consumed. Without it the Messages
	// response stays unchanged and the internal credits field is stripped.
	upstreamCreditsRequestHeader = "X-Sub2API-Upstream-Credits"
	// upstreamCreditsUsageField is the public usage field the credits are
	// exposed under on the Anthropic Messages response when opted in.
	upstreamCreditsUsageField = "upstream_credits"
)

// upstreamCreditsRequested reports whether the caller asked for the upstream
// credits to be included in the Messages usage block.
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
