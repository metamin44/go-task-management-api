package domain

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// validateText rejects empty/whitespace-only values (Unicode-aware, matching
// the DB regex checks) and enforces character limits identical to the
// varchar(n) columns. Values are never modified or echoed in errors.
func validateText(field, s string, max int) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("%s must not be empty or whitespace-only", field)
	}
	if utf8.RuneCountInString(s) > max {
		return fmt.Errorf("%s must be at most %d characters", field, max)
	}
	return nil
}
