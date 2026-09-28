package payment

import (
	"context"
	"errors"
	"time"

	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/services/refunddomestic"
)

var ErrRefundNotFound = errors.New("refund does not exist at provider")

type RefundRequest struct {
	RefundNo, MerchantOrderNo, TransactionID string
	TotalCents, RefundCents                  int64
}
type RefundResult struct {
	RefundNo, MerchantOrderNo, TransactionID, RefundID, Status string
	TotalCents, RefundCents                                    int64
	SuccessAt                                                  time.Time
}
type RefundProvider interface {
	QueryRefund(context.Context, RefundRequest) (RefundResult, error)
	CreateRefund(context.Context, RefundRequest) (RefundResult, error)
}

func (Simulator) QueryRefund(_ context.Context, r RefundRequest) (RefundResult, error) {
	return RefundResult{}, ErrRefundNotFound
}
func (Simulator) CreateRefund(_ context.Context, r RefundRequest) (RefundResult, error) {
	if r.RefundNo == "" || r.TransactionID == "" || r.TotalCents <= 0 || r.RefundCents <= 0 || r.RefundCents > r.TotalCents {
		return RefundResult{}, ErrInvalidPayment
	}
	return RefundResult{RefundNo: r.RefundNo, MerchantOrderNo: r.MerchantOrderNo, TransactionID: r.TransactionID, RefundID: "SIMRF" + r.RefundNo, Status: "SUCCESS", TotalCents: r.TotalCents, RefundCents: r.RefundCents, SuccessAt: time.Now().UTC()}, nil
}
func (w *WechatDirect) QueryRefund(ctx context.Context, r RefundRequest) (RefundResult, error) {
	result, _, err := w.refunds.QueryByOutRefundNo(ctx, refunddomestic.QueryByOutRefundNoRequest{OutRefundNo: core.String(r.RefundNo)})
	var apiErr *core.APIError
	if errors.As(err, &apiErr) && apiErr.Code == "RESOURCE_NOT_EXISTS" {
		return RefundResult{}, ErrRefundNotFound
	}
	if err != nil {
		return RefundResult{}, err
	}
	return refundResult(result)
}
func (w *WechatDirect) CreateRefund(ctx context.Context, r RefundRequest) (RefundResult, error) {
	if r.RefundNo == "" || r.TransactionID == "" || r.TotalCents <= 0 || r.RefundCents <= 0 || r.RefundCents > r.TotalCents {
		return RefundResult{}, ErrInvalidPayment
	}
	result, _, err := w.refunds.Create(ctx, refunddomestic.CreateRequest{TransactionId: core.String(r.TransactionID), OutRefundNo: core.String(r.RefundNo), Amount: &refunddomestic.AmountReq{Total: core.Int64(r.TotalCents), Refund: core.Int64(r.RefundCents), Currency: core.String("CNY")}})
	if err != nil {
		return RefundResult{}, err
	}
	return refundResult(result)
}
func refundResult(r *refunddomestic.Refund) (RefundResult, error) {
	if r == nil || r.OutRefundNo == nil || r.OutTradeNo == nil || r.TransactionId == nil || r.RefundId == nil || r.Status == nil || r.Amount == nil || r.Amount.Total == nil || r.Amount.Refund == nil || r.Amount.Currency == nil || *r.Amount.Currency != "CNY" {
		return RefundResult{}, ErrInvalidPayment
	}
	out := RefundResult{RefundNo: *r.OutRefundNo, MerchantOrderNo: *r.OutTradeNo, TransactionID: *r.TransactionId, RefundID: *r.RefundId, Status: string(*r.Status), TotalCents: *r.Amount.Total, RefundCents: *r.Amount.Refund}
	if r.SuccessTime != nil {
		out.SuccessAt = r.SuccessTime.UTC()
	}
	if out.Status == "SUCCESS" && out.SuccessAt.IsZero() {
		return RefundResult{}, ErrInvalidPayment
	}
	return out, nil
}
