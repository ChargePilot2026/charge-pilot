package finance

import "errors"

type Funding struct {
	MemberCents Money
	CouponCents Money
	WalletCents Money
	WechatCents Money
}

var ErrInvalidFunding = errors.New("negative amount or credit")

// Fund follows the documented precedence: membership, coupon, wallet, WeChat.
func Fund(total, member, coupon, wallet Money) (Funding, error) {
	if total < 0 || member < 0 || coupon < 0 || wallet < 0 {
		return Funding{}, ErrInvalidFunding
	}
	remaining := total
	take := func(available Money) Money {
		if available > remaining {
			available = remaining
		}
		remaining -= available
		return available
	}
	result := Funding{
		MemberCents: take(member),
		CouponCents: take(coupon),
		WalletCents: take(wallet),
	}
	result.WechatCents = remaining
	return result, nil
}
