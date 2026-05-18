package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/seakee/cpa-manager/usage-service/internal/store"
)

// rateLimitErrorResponse is the ready-to-forward 429 response that the
// CPA proxy can return to the upstream client when a key's per-key limit
// is reached. The shape follows Anthropic's published rate-limit
// response so that Claude SDK clients can parse it transparently.
//
// See https://docs.anthropic.com/en/api/rate-limits for the field
// conventions reproduced here.
type rateLimitErrorResponse struct {
	Status  int                    `json:"status"`
	Headers map[string]string      `json:"headers"`
	Body    map[string]interface{} `json:"body"`
}

// buildClaudeRateLimitResponse renders a Claude-style 429 payload for a
// blocked key. resetAt is derived from the sliding window so the client
// can honor Retry-After / anthropic-ratelimit-tokens-reset.
func buildClaudeRateLimitResponse(item store.APIKeyLimitWithUsage) rateLimitErrorResponse {
	var retryAfter int64
	var resetISO string
	if item.ResetAtMS > 0 {
		retryAfter = (item.ResetAtMS - time.Now().UnixMilli()) / 1000
		if retryAfter < 0 {
			retryAfter = 0
		}
		resetISO = time.UnixMilli(item.ResetAtMS).UTC().Format(time.RFC3339)
	}

	used := float64(item.UsedTokens)
	if item.LimitType == "cost" {
		used = item.UsedCost
	}
	remaining := item.LimitValue - used
	if remaining < 0 {
		remaining = 0
	}

	headers := map[string]string{
		"Content-Type": "application/json",
		"Retry-After":  strconv.FormatInt(retryAfter, 10),
		"anthropic-ratelimit-tokens-limit":     formatLimitNumber(item.LimitValue, item.LimitType),
		"anthropic-ratelimit-tokens-remaining": formatLimitNumber(remaining, item.LimitType),
	}
	if resetISO != "" {
		headers["anthropic-ratelimit-tokens-reset"] = resetISO
	}

	var message string
	if item.LimitType == "cost" {
		message = fmt.Sprintf(
			"Rate limit exceeded: $%.2f cost limit reached for the last %d day(s).",
			item.LimitValue, item.WindowDays,
		)
	} else {
		message = fmt.Sprintf(
			"Rate limit exceeded: %s token limit reached for the last %d day(s).",
			formatTokenCount(item.LimitValue), item.WindowDays,
		)
	}
	if retryAfter > 0 {
		message += fmt.Sprintf(" Retry after %s.", formatHumanDuration(retryAfter))
	}

	return rateLimitErrorResponse{
		Status:  http.StatusTooManyRequests,
		Headers: headers,
		Body: map[string]interface{}{
			"type": "error",
			"error": map[string]interface{}{
				"type":    "rate_limit_error",
				"message": message,
			},
		},
	}
}

func formatLimitNumber(value float64, limitType string) string {
	if limitType == "cost" {
		return strconv.FormatFloat(value, 'f', 4, 64)
	}
	return strconv.FormatFloat(value, 'f', 0, 64)
}

func formatTokenCount(value float64) string {
	whole := int64(value)
	if whole < 1000 {
		return strconv.FormatInt(whole, 10)
	}
	// Pretty-print thousand separators without pulling in extra deps.
	negative := whole < 0
	if negative {
		whole = -whole
	}
	digits := strconv.FormatInt(whole, 10)
	result := make([]byte, 0, len(digits)+len(digits)/3)
	for i, c := range []byte(digits) {
		if i > 0 && (len(digits)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, c)
	}
	if negative {
		return "-" + string(result)
	}
	return string(result)
}

func formatHumanDuration(seconds int64) string {
	if seconds <= 0 {
		return "0s"
	}
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	mins := (seconds % 3600) / 60
	secs := seconds % 60
	switch {
	case days > 0:
		if hours > 0 {
			return fmt.Sprintf("%dd %dh", days, hours)
		}
		return fmt.Sprintf("%dd", days)
	case hours > 0:
		if mins > 0 {
			return fmt.Sprintf("%dh %dm", hours, mins)
		}
		return fmt.Sprintf("%dh", hours)
	case mins > 0:
		return fmt.Sprintf("%dm", mins)
	default:
		return fmt.Sprintf("%ds", secs)
	}
}
