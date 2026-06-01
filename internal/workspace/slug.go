package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var labelValuePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_.-]{0,61}[A-Za-z0-9])?$`)
var dns1123LabelPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
var dns1123SubdomainPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

func Slug(source string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(source) {
		allowed := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if allowed {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if r == '-' || r == '_' || r == '.' || unicode.IsSpace(r) {
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func DNSLabel(source string) string {
	slug := Slug(source)
	if len(slug) <= 63 {
		return slug
	}
	sum := sha256.Sum256([]byte(slug))
	prefix := hex.EncodeToString(sum[:])[:10]
	return slug[:52] + "-" + prefix
}

func ValidateLabelValue(field, value string) error {
	if !labelValuePattern.MatchString(value) {
		return fmt.Errorf("%s must be a Kubernetes label value: non-empty, <=63 chars, alphanumeric at both ends, and only alphanumeric, '-', '_', or '.' inside", field)
	}
	return nil
}

func ValidateDNS1123Label(field, value string) error {
	if !dns1123LabelPattern.MatchString(value) {
		return fmt.Errorf("%s must be a DNS-1123 label: non-empty, <=63 chars, lower-case alphanumeric or '-', and alphanumeric at both ends", field)
	}
	return nil
}

func ValidateDNS1123Subdomain(field, value string) error {
	if len(value) > 253 || !dns1123SubdomainPattern.MatchString(value) {
		return fmt.Errorf("%s must be a DNS-1123 subdomain: non-empty, <=253 chars, lower-case alphanumeric, '-' or '.', and alphanumeric at segment ends", field)
	}
	return nil
}
