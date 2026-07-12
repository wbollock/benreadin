package services

import (
	"strings"
	"testing"
)

func TestGoodreadsStatusErrorMessages(t *testing.T) {
	tests := []struct {
		code int
		want string // substring the user-facing message must contain
	}{
		{404, "public"},
		{403, "private"},
		{401, "private"},
		{429, "rate-limiting"},
		{503, "having trouble"},
		{418, "HTTP 418"},
	}
	for _, tt := range tests {
		msg := (&GoodreadsStatusError{Code: tt.code}).Error()
		if !strings.Contains(msg, tt.want) {
			t.Errorf("status %d: message %q does not mention %q", tt.code, msg, tt.want)
		}
		if strings.Contains(msg, "goodreads returned") {
			t.Errorf("status %d: message %q leaks the old internal wording", tt.code, msg)
		}
	}
}
