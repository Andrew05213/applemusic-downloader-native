package appliance

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

var (
	bearerPattern                 = regexp.MustCompile(`(?i)(\bauthorization\s*:\s*bearer\s+)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;\r\n]+)`)
	assignmentPattern             = regexp.MustCompile(`(?i)(\b(?:apple[_-]?(?:email|password)|media-user-token|authorization-token|token|password|secret|credential(?:s)?)\s*[=:]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s&,;\r\n]+)`)
	legacyCredentialLengthPattern = regexp.MustCompile(`(?i)\b(?:written|total)=[0-9]+\b`)
	legacyLoginFDLengthPattern    = regexp.MustCompile(`(?i)\blogin_fd_eof[ \t]+bytes=[0-9]+\b`)
)

type Redactor struct {
	mu      sync.RWMutex
	secrets []string
}

func (r *Redactor) SetSecrets(values ...string) {
	seen := make(map[string]struct{}, len(values))
	secrets := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		secrets = append(secrets, value)
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	r.mu.Lock()
	r.secrets = secrets
	r.mu.Unlock()
}

func (r *Redactor) Sanitize(value string) string {
	r.mu.RLock()
	secrets := append([]string(nil), r.secrets...)
	r.mu.RUnlock()
	for _, secret := range secrets {
		value = strings.ReplaceAll(value, secret, "[redacted]")
	}
	value = bearerPattern.ReplaceAllString(value, "${1}[redacted]")
	value = assignmentPattern.ReplaceAllString(value, "${1}[redacted]")
	value = legacyLoginFDLengthPattern.ReplaceAllString(value, "login_fd_eof")
	value = legacyCredentialLengthPattern.ReplaceAllString(value, "")
	return value
}

type LogBuffer struct {
	mu       sync.RWMutex
	lines    []string
	redactor *Redactor
}

func NewLogBuffer(redactor *Redactor) *LogBuffer {
	if redactor == nil {
		redactor = &Redactor{}
	}
	return &LogBuffer{redactor: redactor}
}

func (b *LogBuffer) Add(line string) {
	line = strings.TrimSpace(b.redactor.Sanitize(line))
	if line == "" {
		return
	}
	if len(line) > 16*1024 {
		line = line[:16*1024] + "…"
	}
	b.mu.Lock()
	if len(b.lines) >= MaxDiagnosticLog {
		copy(b.lines, b.lines[len(b.lines)-MaxDiagnosticLog+1:])
		b.lines = b.lines[:MaxDiagnosticLog-1]
	}
	b.lines = append(b.lines, line)
	b.mu.Unlock()
}

// Lines sanitizes a second time at the diagnostic boundary.  This protects
// older buffered lines when the credential set changes after they were read.
func (b *LogBuffer) Lines() []string {
	b.mu.RLock()
	lines := append([]string(nil), b.lines...)
	b.mu.RUnlock()
	for index := range lines {
		lines[index] = b.redactor.Sanitize(lines[index])
	}
	return lines
}
