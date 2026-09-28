package payment

import (
	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/services/refunddomestic"
	"testing"
	"time"
)

func TestRefundResultRequiresCompleteVerifiedAmounts(t *testing.T) {
	r := &refunddomestic.Refund{OutRefundNo: core.String("RF1"), OutTradeNo: core.String("PAY1"), TransactionId: core.String("TX1"), RefundId: core.String("WXRF1"), Status: refunddomestic.Status("SUCCESS").Ptr(), SuccessTime: core.Time(time.Now()), Amount: &refunddomestic.Amount{Total: core.Int64(100), Refund: core.Int64(20), Currency: core.String("CNY")}}
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
	r.Amount.Currency = core.String("USD")
	if _, err := refundResult(r); err == nil {
		t.Fatal("foreign currency accepted")
	}
	r.Amount = nil
	if _, err := refundResult(r); err == nil {
		t.Fatal("missing amounts accepted")
	}
}
