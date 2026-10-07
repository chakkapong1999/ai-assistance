package review

import (
	"regexp"
	"strings"
)

const redacted = "[REDACTED]"

// secretPatterns replace the secret part only, so the line keeps its shape.
// Each regexp has the secret in group 1 when it has a group, otherwise the
// whole match is replaced.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),                                           // AWS access key id
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{30,}`),                                 // GitHub tokens
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{30,}`),                               // GitHub fine-grained
	regexp.MustCompile(`xox[abprs]-[A-Za-z0-9-]{10,}`),                               // Slack
	regexp.MustCompile(`AT(?:BB|CTT)[A-Za-z0-9_=\-]{20,}`),                           // Bitbucket app password / access token
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`), // JWT
	regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/=-]{16,})`),                   // Authorization: Bearer ...
	regexp.MustCompile(`://[^\s/:@]+:([^\s/@]{3,})@`),                                // credentials in URLs
	// key = "value" (quoted: any value of 6+ chars)
	regexp.MustCompile(`(?i)(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)["']?\s*[:=]\s*["']([^"'\s]{6,})["']`),
	// KEY=value (unquoted, .env style: must contain a digit so `token: string` is left alone)
	regexp.MustCompile(`(?i)(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)["']?\s*[:=]\s*([A-Za-z0-9_+/=.-]{3,}[0-9][A-Za-z0-9_+/=.-]{3,})`),
}

var (
	pemBegin = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
	pemEnd   = regexp.MustCompile(`-----END [A-Z ]*PRIVATE KEY-----`)
)

// MaskSecrets hides likely credentials in a raw diff before it goes anywhere
// near an LLM or the database. It works line by line and never adds or
// removes lines, so diff structure and line numbers stay valid.
//
// This is a best-effort safety net, not a guarantee; it does not replace
// keeping secrets out of the repository.
func MaskSecrets(diff string) string {
	lines := strings.Split(diff, "\n")
	inKey := false
	for i, ln := range lines {
		// Keep the diff prefix (+, -, space) so the line still parses.
		prefix, body := "", ln
		if ln != "" && (ln[0] == '+' || ln[0] == '-' || ln[0] == ' ') {
			prefix, body = ln[:1], ln[1:]
		}

		if strings.HasPrefix(ln, "@@") || strings.HasPrefix(ln, "diff --git ") {
			inKey = false // a truncated key block must not swallow the rest of the diff
		}

		switch {
		case inKey:
			if pemEnd.MatchString(body) {
				inKey = false
				lines[i] = prefix + body
			} else {
				lines[i] = prefix + "[REDACTED PRIVATE KEY]"
			}
			continue
		case pemBegin.MatchString(body):
			if !pemEnd.MatchString(body) {
				inKey = true
			}
			lines[i] = prefix + body
			continue
		}

		for _, re := range secretPatterns {
			body = re.ReplaceAllStringFunc(body, func(m string) string {
				sub := re.FindStringSubmatch(m)
				if len(sub) > 1 && sub[1] != "" {
					return strings.Replace(m, sub[1], redacted, 1)
				}
				return redacted
			})
		}
		lines[i] = prefix + body
	}
	return strings.Join(lines, "\n")
}
