package reverseproxy

import (
	"slices"
	"strings"
	"testing"
)

func TestLookupAndSupport(t *testing.T) {
	t.Parallel()

	p, err := Lookup(typeAngie)
	if err != nil {
		t.Fatalf("expected provider for %q, got %v", typeAngie, err)
	}
	if p.Type() != typeAngie {
		t.Fatalf("expected type %q, got %q", typeAngie, p.Type())
	}
	if !IsSupported(typeAngie) {
		t.Fatalf("expected IsSupported(%q) to be true", typeAngie)
	}
	if IsSupported("nginx") {
		t.Fatal("expected IsSupported(\"nginx\") to be false")
	}
	if !slices.Equal(SupportedTypes(), []string{typeAngie}) {
		t.Fatalf("expected SupportedTypes [%q], got %v", typeAngie, SupportedTypes())
	}
	if _, lookupErr := Lookup("nginx"); lookupErr == nil || !strings.Contains(lookupErr.Error(), "unsupported reverse-proxy provider type") {
		t.Fatalf("expected unsupported provider error, got %v", lookupErr)
	}
}
