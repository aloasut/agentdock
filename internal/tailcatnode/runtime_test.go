package tailcatnode

import (
	"strings"
	"testing"
)

func TestIdentityRoundTripKeepsPresharedKey(t *testing.T) {
	raw, public, err := newIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(public, "psk:") || strings.Contains(public, "privkey:") {
		t.Fatal("public identity text contains a secret")
	}
	decoded, err := decodeIdentity(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Private.Public().String() != public {
		t.Fatalf("public key = %s, want %s", decoded.Private.Public(), public)
	}
	if decoded.Public.PresharedKey.IsZero() {
		t.Fatal("decoded identity dropped the preshared key")
	}
	raw[len(raw)-2] = '{'
	if _, err := decodeIdentity(raw); err == nil {
		t.Fatal("trailing data was accepted")
	}
}
