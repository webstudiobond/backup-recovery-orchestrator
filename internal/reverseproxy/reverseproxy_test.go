package reverseproxy

import (
	"slices"
	"strings"
	"testing"
)

func TestLookupAndSupport(t *testing.T) {
	t.Parallel()

	p, err := Lookup(TypeAngie)
	if err != nil {
		t.Fatalf("expected provider for %q, got %v", TypeAngie, err)
	}
	if p.Type() != TypeAngie {
		t.Fatalf("expected type %q, got %q", TypeAngie, p.Type())
	}
	if !IsSupported(TypeAngie) {
		t.Fatalf("expected IsSupported(%q) to be true", TypeAngie)
	}
	if IsSupported("nginx") {
		t.Fatal("expected IsSupported(\"nginx\") to be false")
	}
	if !slices.Equal(SupportedTypes(), []string{TypeAngie}) {
		t.Fatalf("expected SupportedTypes [%q], got %v", TypeAngie, SupportedTypes())
	}
	if _, lookupErr := Lookup("nginx"); lookupErr == nil || !strings.Contains(lookupErr.Error(), "unsupported reverse-proxy provider type") {
		t.Fatalf("expected unsupported provider error, got %v", lookupErr)
	}
}
