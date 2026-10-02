package admin

import (
	"context"

	settlementpkg "github.com/ChargePilot2026/charge-pilot/internal/central/settlement"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerInvoices 挂载开票审核的三个接口。列表只读，要求 finance.read；审批和驳回
// 走同一个 handler，由路由路径区分动作，两者都要求 invoice.review。
func (a ResourceAPI) registerInvoices(r *gin.Engine) {
	r.GET("/api/v1/admin/billing/invoices", a.Auth.Require("finance.read"), a.invoices)
	r.POST("/api/v1/admin/billing/invoices/:id/approve", a.Auth.Require("invoice.review"), a.reviewInvoice)
	r.POST("/api/v1/admin/billing/invoices/:id/reject", a.Auth.Require("invoice.review"), a.reviewInvoice)
}

// invoices 返回开票申请列表，左连 invoice_admin_review 汇总双审进度。带 queue_ 前缀
// 的三个字段是汇总后的当前值（优先取审核表，没有再回退到申请单本身），因为审核进度
// 可能只记在审核表里。审核人 ID 统一转成字符串输出，避免 JS 大整数精度丢失。
func (a ResourceAPI) invoices(c *gin.Context) {
	rows := []map[string]any{}
	err := a.Store.UserDB.WithContext(c.Request.Context()).Table("invoice_request i").Select("i.id AS invoice_request_id,i.invoice_no,i.user_id,i.biz_type,i.biz_id,i.total_cents,i.invoice_type,i.title,i.tax_no,i.email,i.review_status,COALESCE(r.review_status,i.review_status) AS queue_review_status,r.reject_reason AS queue_reject_reason,r.first_reviewer_id,r.second_reviewer_id,i.reject_reason,COALESCE(r.invoice_url,i.invoice_url) AS invoice_url,i.created_at").Joins("LEFT JOIN invoice_admin_review r ON r.invoice_request_id=i.id").Where("i.deleted_at IS NULL").Order("i.id DESC").Find(&rows).Error
	if err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(rows)
	stringIDs(rows, "user_id", "first_reviewer_id", "second_reviewer_id")
	httpapi.OK(c, gin.H{"items": rows})
}

// financeActor 校验当前管理员为 customer_finance 且持有指定审核权限，失败返回 403。
// 发票审核和计量核实共用此校验。
func (a ResourceAPI) financeActor(c *gin.Context, permission string) (Profile, bool) {
	p := c.MustGet("admin_profile").(Profile)
	if p.Role != "customer_finance" || !hasPermission(p, permission) {
		httpapi.Write(c, 403, 1003, "须由拥有审核权限的客户财务人员执行", nil)
		return p, false
	}
	return p, true
}

// invoiceReviewStore 装配 settlement 家族的开票双审存储；
// 第二签的实时身份核对回读 admin 授权域的账号档案与权限。
func (a ResourceAPI) invoiceReviewStore() settlementpkg.InvoiceReviewStore {
	return settlementpkg.InvoiceReviewStore{
		DB: a.Store.UserDB,
		LookupReviewer: func(ctx context.Context, reviewerID uint64) (settlementpkg.ReviewerStatus, error) {
			p, err := a.Auth.Store.Profile(ctx, reviewerID)
			if err != nil {
				return settlementpkg.ReviewerStatus{}, err
			}
			return settlementpkg.ReviewerStatus{Role: p.Role, Permitted: hasPermission(p, "invoice.review")}, nil
		},
	}
}

// reviewInvoice 处理审批和驳回。双审与申请单状态机的写入归属 settlement 家族，
// 本 handler 只做输入校验、权限、审计与错误映射。
func (a ResourceAPI) reviewInvoice(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	p, ok := a.financeActor(c, "invoice.review")
	if !ok {
		return
	}
	// 请求体随动作而变：审批只读 invoice_url，驳回只读 reason，两者的必填项由下方校验区分。
	var in struct {
		InvoiceURL string `json:"invoice_url"` // 审批时必填的 HTTPS 发票链接。
		Reason     string `json:"reason"`      // 驳回原因，最长 255 字符；审批时可留空。
	}
	if !decodeResource(c, &in) {
		return
	}
	approve := c.FullPath() == "/api/v1/admin/billing/invoices/:id/approve"
	if (approve && !httpsURL(in.InvoiceURL)) || (!approve && !validText(in.Reason, 255)) {
		httpapi.BadRequest(c, "请填写 HTTPS 发票链接或拒绝原因")
		return
	}
	status := ""
	var before map[string]any
	var auditPending []auditEntry
	err := a.auditedTransaction(c, a.Store.UserDB, &auditPending, func(tx *gorm.DB) error {
		var err error
		status, before, err = a.invoiceReviewStore().Review(c.Request.Context(), tx, id, settlementpkg.InvoiceDecision{
			Approve: approve, InvoiceURL: in.InvoiceURL, Reason: in.Reason, ActorID: p.ID,
		})
		if err != nil {
			return err
		}
		auditPending = []auditEntry{{status, "invoice", id, before, in, ""}}
		return nil
	})
	if err != nil {
		resourceFailure(c, asConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"review_status": status})
}
