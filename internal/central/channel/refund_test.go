package channel

import (
	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/services/refunddomestic"
	"testing"
	"time"
)

func TestRefundResultRequiresCompleteVerifiedAmounts(t *testing.T) {
	r := &refunddomestic.Refund{OutRefundNo: new("RF1"), OutTradeNo: new("PAY1"), TransactionId: new("TX1"), RefundId: new("WXRF1"), Status: refunddomestic.Status("SUCCESS").Ptr(), SuccessTime: new(time.Now()), Amount: &refunddomestic.Amount{Total: core.Int64(100), Refund: core.Int64(20), Currency: new("CNY")}}
	if result, err := refundResult(r); err != nil || result.RefundCents != 20 || result.TotalCents != 100 {
		t.Fatal(result, err)
	}
	r.SuccessTime = nil
	if _, err := refundResult(r); err == nil {
		t.Fatal("success without timestamp accepted")
	}
	r.Status = refunddomestic.Status("PROCESSING").Ptr()
	if _, err := refundResult(r); err != nil {
		t.Fatal(err)
	}
	r.Amount.Currency = new("USD")
	if _, err := refundResult(r); err == nil {
		t.Fatal("foreign currency accepted")
	}
	r.Amount = nil
	if _, err := refundResult(r); err == nil {
		t.Fatal("missing amounts accepted")
	}
}
