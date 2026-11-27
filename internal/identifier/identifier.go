// Package identifier validates configuration and stack identifier tokens against path-traversal and flag-injection characters.
package identifier

import (
	"fmt"
	"strings"
)

// Validate verifies that value is non-empty, safe against path traversal or flag injection, and composed of allowed identifier runes.
func Validate(value, label string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", label)
	}
	if strings.HasPrefix(value, ".") || strings.HasPrefix(value, "-") || strings.Contains(value, "..") {
		return fmt.Errorf("%s %q must not start with '.' or '-' or contain '..'", label, value)
	}
	for _, r := range value {
		if !isAllowedIdentifierRune(r) {
			return fmt.Errorf("%s %q contains invalid character %q", label, value, r)
		}
	}
	return nil
}

// ValidateList verifies every token in values using Validate under the provided field label.
func ValidateList(values []string, label string) error {
	for _, value := range values {
		if err := Validate(value, label); err != nil {
			return err
		}
	}
	return nil
}

func isAllowedIdentifierRune(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r == '_' || r == '-' || r == '.'
}
