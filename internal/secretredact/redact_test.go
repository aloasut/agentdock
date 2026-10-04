package secretredact

import (
	"strings"
	"testing"
)

func TestTextRedactsTailcatSecrets(t *testing.T) {
	input := "dial failed psk:abcd privkey:0011 address tcAbcdefghijklmnop and tc-short stays"
	got := Text(input)
	for _, secret := range []string{"psk:abcd", "privkey:0011", "tcAbcdefghijklmnop"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted text still contains %s: %s", secret, got)
		}
	}
	if !strings.Contains(got, "tc-short stays") || !strings.Contains(got, "[redacted]") {
		t.Fatalf("redacted text = %s", got)
	}
}
