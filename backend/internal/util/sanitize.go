package util

import (
	"net/url"
	"regexp"
	"strings"
)

// SanitizeMessage redacts URL credentials, query strings, path-segment
// secrets, and common token/key patterns from a free-form message before it
// is persisted (e.g. alert_deliveries.last_error, report.last_err) or sent to
// external channels (webhook/Slack/Telegram).
//
// The function unifies what was previously two diverging implementations:
//   - alerting/retry.go sanitizeDeliveryError: URL redaction + token regex
//     (covered all webhook/feishu/dingtalk patterns but missed Telegram bot
//     token format and was packaged-private)
//   - util.SanitizeDeliveryError: only Telegram-aware (returned err.Error()
//     unchanged for every other channel type)
//
// Now both call sites use SanitizeMessage so:
//   - dispatcher.go first-attempt failures
//   - retry.go retry failures
//   - reporting.last_err → external dispatch
//
// all share identical, comprehensive sanitization. Output is bounded to
// 500 runes (truncated with ellipsis) to keep DB columns and external
// payloads predictable.
func SanitizeMessage(msg string) string {
	if msg == "" {
		return ""
	}
	msg = redactURLs(msg)
	msg = botTokenSanitizer.ReplaceAllString(msg, "bot***:***")
	for _, re := range sensitivePatterns {
		msg = re.ReplaceAllString(msg, "$1=***")
	}
	if utf8RuneCount(msg) > 500 {
		runes := []rune(msg)
		msg = string(runes[:500]) + "…"
	}
	return msg
}

// SanitizeError is the typed convenience wrapper around SanitizeMessage for
// error values; returns "" when err is nil.
func SanitizeError(err error) string {
	if err == nil {
		return ""
	}
	return SanitizeMessage(err.Error())
}

// urlLikePattern matches http(s)/ws(s) URLs embedded in free text. Used to
// redact webhook targets that carry bearer tokens in path or query.
var urlLikePattern = regexp.MustCompile(`(https?|wss?)://[^\s"'<>]+`)

// sensitivePatterns matches tokens/secrets in key=value or key: value form.
// Captures the key name and replaces the value with "***".
var sensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization|bearer|token|api[_-]?key|secret|password)[=:]\s*[^\s"',;)]+`),
}

// redactURLs drops credentials, query strings, and path-segment secrets from
// any http(s)/ws URL embedded in msg. Webhook targets (Slack /services/T/B/X,
// Feishu /open-apis/bot/v2/hook/<token>, DingTalk /robot/send?access_token=...,
// Telegram /bot<token>/sendMessage, etc.) routinely carry bearer tokens in the
// URL *path*, so keeping scheme+host alone is what's safe to persist. Query
// strings are also redacted (DingTalk's access_token lives there).
func redactURLs(msg string) string {
	return urlLikePattern.ReplaceAllStringFunc(msg, func(match string) string {
		u, err := url.Parse(match)
		if err != nil {
			return match
		}
		if u.User != nil {
			u.User = url.User("***")
		}
		if u.RawQuery != "" {
			u.RawQuery = "***"
		}
		// Path can contain tokens — truncate to "/***" when non-trivial.
		// A bare "/" or empty path is fine (no secrets).
		if u.Path != "" && u.Path != "/" {
			u.Path = "/***"
		}
		return strings.TrimSuffix(u.String(), "?")
	})
}

func utf8RuneCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
