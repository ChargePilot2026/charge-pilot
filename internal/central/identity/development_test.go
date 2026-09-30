package identity

import (
	"context"
	"testing"
)

func TestDevelopmentIdentityRequiresExplicitAccountNamespace(t *testing.T) {
	e := DevelopmentExchanger{}
	for _, code := range []string{"wechat-code", "dev:", "dev:../admin", "dev:user name"} {
		if _, err := e.Exchange(context.Background(), code); err == nil {
			t.Fatalf("accepted %q", code)
		}
	}
	a, err := e.Exchange(context.Background(), "dev:tester-1")
	b, _ := e.Exchange(context.Background(), "dev:tester-1")
	if err != nil || a.OpenID != "development:tester-1" || a != b {
		t.Fatal(a, b, err)
	}
}
