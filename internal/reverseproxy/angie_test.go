package reverseproxy

import (
	"slices"
	"testing"
)

func TestAngieProvider(t *testing.T) {
	t.Parallel()

	p := angieProvider{}
	if p.Type() != typeAngie {
		t.Fatalf("expected type %q, got %q", typeAngie, p.Type())
	}
	if !slices.Equal(p.SyntaxCheckArgs(), []string{typeAngie, "-t"}) {
		t.Fatalf("unexpected syntax args: %v", p.SyntaxCheckArgs())
	}

	confPolicy := p.DefaultConfDACPolicy("proxy-grp")
	if confPolicy.Owner != "root:proxy-grp" || confPolicy.FileMode != "0440" || confPolicy.Recursive || confPolicy.DirMode != "" {
		t.Fatalf("unexpected default conf policy: %+v", confPolicy)
	}

	certPolicy := p.DefaultCertDACPolicy("proxy-usr")
	if certPolicy.Owner != "proxy-usr:proxy-usr" || !certPolicy.Recursive || certPolicy.DirMode != "0700" || certPolicy.FileMode != "0600" {
		t.Fatalf("unexpected default cert policy: %+v", certPolicy)
	}
}
