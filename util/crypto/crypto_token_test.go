package crypto

import "testing"

func TestHashTokenSHA256StableAndDistinct(t *testing.T) {
	a := HashTokenSHA256("hello-token")
	b := HashTokenSHA256("hello-token")
	c := HashTokenSHA256("other-token")
	if a != b {
		t.Fatalf("hash not stable")
	}
	if a == c {
		t.Fatalf("different inputs hashed equal")
	}
	if len(a) != 64 {
		t.Fatalf("expected 64 hex chars, got %d", len(a))
	}
}
