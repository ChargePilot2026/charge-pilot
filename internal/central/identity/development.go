package identity

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// DevelopmentExchanger is wired only with explicit LOGIN_MODE=development.
// It uses a distinct openid namespace and never contacts WeChat.
type DevelopmentExchanger struct{}

var developmentAccount = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func (DevelopmentExchanger) Exchange(_ context.Context, code string) (WeChatIdentity, error) {
	if !strings.HasPrefix(code, "dev:") || !developmentAccount.MatchString(strings.TrimPrefix(code, "dev:")) {
		return WeChatIdentity{}, fmt.Errorf("invalid development account")
	}
	return WeChatIdentity{OpenID: "development:" + strings.TrimPrefix(code, "dev:")}, nil
}
