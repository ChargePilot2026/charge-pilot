package identity

import (
	"context"
	"errors"

	"github.com/go-pay/wechat-sdk/mini"
)

type WeChatIdentity struct{ OpenID, UnionID string }

type CodeExchanger interface {
	Exchange(context.Context, string) (WeChatIdentity, error)
}

type MiniProgram struct{ SDK *mini.SDK }

func (m MiniProgram) Exchange(ctx context.Context, code string) (WeChatIdentity, error) {
	session, err := m.SDK.Code2Session(ctx, code)
	if err != nil {
		return WeChatIdentity{}, err
	}
	if session.Openid == "" {
		return WeChatIdentity{}, errors.New("WeChat returned no openid")
	}
	return WeChatIdentity{OpenID: session.Openid, UnionID: session.Unionid}, nil
}
