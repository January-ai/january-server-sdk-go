package january

import (
	"fmt"
	"strings"
	"unicode/utf16"
)

func validateCreateInput(input CreateClientTokenInput) error {
	if strings.TrimSpace(input.EndUserID) == "" {
		return fmt.Errorf("%w: EndUserID must be derived from the authenticated user", ErrInvalidInput)
	}
	if len(utf16.Encode([]rune(strings.TrimSpace(input.EndUserID)))) > 64 {
		return fmt.Errorf("%w: EndUserID must be at most 64 UTF-16 code units", ErrInvalidInput)
	}
	if len(input.Scopes) == 0 || len(input.Scopes) > 6 {
		return fmt.Errorf("%w: Scopes must contain 1–6 client-grantable scopes", ErrInvalidInput)
	}
	for _, scope := range input.Scopes {
		switch scope {
		case ScopeFoodsRead, ScopeFoodAnalysisWrite, ScopeFoodLogsRead, ScopeFoodLogsWrite, ScopeGlucoseRead, ScopeRestaurantsRead:
		default:
			return fmt.Errorf("%w: unsupported client scope %q", ErrInvalidInput, scope)
		}
	}
	if input.TTLSeconds != nil && (*input.TTLSeconds < 300 || *input.TTLSeconds > 7200) {
		return fmt.Errorf("%w: TTLSeconds must be from 300 through 7200", ErrInvalidInput)
	}
	return nil
}
