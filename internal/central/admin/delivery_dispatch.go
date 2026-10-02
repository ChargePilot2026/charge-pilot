package admin

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/delivery"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// WebhookDispatch 承接 worker 投递侧需要的 webhook 状态读写：
// 订阅清单读取与投递日志落库。配置写仍走 /api/v1/admin/webhooks，
// 投递日志每个订阅 + 事件只占一行，重试时就地更新，attempt_count 自增。
type WebhookDispatch struct {
	AdminDB *gorm.DB
}

// WebhookSubscription 是启用中的订阅，Secret 仅在内部网络上明文传输。
type WebhookSubscription struct {
	ID         uint64   `json:"id"`
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	Secret     string   `json:"secret"`
	EventTypes []string `json:"event_types" gorm:"-"`
}

// ActiveSubscriptions 返回全部启用订阅并解码 event_types 的 JSON 列。
// 投递方按事件类型自行匹配；无效 JSON 视为不匹配任何事件，不中止清单。
func (w WebhookDispatch) ActiveSubscriptions(ctx context.Context) ([]WebhookSubscription, error) {
	rows := []WebhookSubscription{}
	if err := w.AdminDB.WithContext(ctx).Table("webhook_subscription").
		Select("id, name, url, secret").Where("enabled = 1 AND deleted_at IS NULL").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]uint64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	var types []struct {
		ID         uint64 `gorm:"column:id"`
		EventTypes []byte `gorm:"column:event_types"`
	}
	if err := w.AdminDB.WithContext(ctx).Table("webhook_subscription").
		Select("id, CAST(event_types AS CHAR) AS event_types").
		Where("id IN ?", ids).Find(&types).Error; err != nil {
		return nil, err
	}
	byID := map[uint64][]string{}
	for _, row := range types {
		var decoded []string
		if len(row.EventTypes) > 0 {
			_ = json.Unmarshal(row.EventTypes, &decoded)
		}
		byID[row.ID] = decoded
	}
	for i := range rows {
		rows[i].EventTypes = byID[rows[i].ID]
	}
	return rows, nil
}

// RecordDeliveries 在一个事务里批量 upsert 投递结果。与旧 worker 版语义一致：
// 无论成功失败都写日志（投递日志就是审计凭证），重试覆盖旧行，
// attempt_count 由数据库自增，不由投递方计算。
func (w WebhookDispatch) RecordDeliveries(ctx context.Context, records []delivery.DeliveryRecord) error {
	if len(records) == 0 {
		return nil
	}
	now := time.Now().UTC()
	rows := make([]map[string]any, 0, len(records))
	for _, record := range records {
		var statusValue any
		if record.ResponseStatus != nil {
			statusValue = *record.ResponseStatus
		}
		var responseValue any
		if record.ResponseBody != nil {
			responseValue = *record.ResponseBody
		}
		var errorValue any
		if record.Error != "" {
			errorValue = record.Error
		}
		rows = append(rows, map[string]any{
			"subscription_id": record.SubscriptionID,
			"event_id":        record.EventID,
			"event_type":      record.EventType,
			"request_body":    record.RequestBody,
			"response_status": statusValue,
			"response_body":   responseValue,
			"error_msg":       errorValue,
			"attempt_count":   1,
			"duration_ms":     record.DurationMS,
			"delivered_at":    now,
		})
	}
	return w.AdminDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Table("webhook_delivery_log").Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "subscription_id"}, {Name: "event_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"event_type":      gorm.Expr("VALUES(event_type)"),
				"request_body":    gorm.Expr("VALUES(request_body)"),
				"response_status": gorm.Expr("VALUES(response_status)"),
				"response_body":   gorm.Expr("VALUES(response_body)"),
				"error_msg":       gorm.Expr("VALUES(error_msg)"),
				"attempt_count":   gorm.Expr("webhook_delivery_log.attempt_count + 1"),
				"duration_ms":     gorm.Expr("VALUES(duration_ms)"),
				"delivered_at":    gorm.Expr("VALUES(delivered_at)"),
			}),
		}).Create(&rows).Error
	})
}

// RegulatoryDispatch 承接监管报送的租约领取与状态推进，全部在 central
// 单库事务内完成；worker 只经 Sender 签名发送，按 lease_token 回执结果。
type RegulatoryDispatch struct {
	AdminDB *gorm.DB
}

// ClaimedReport 是一笔领取到的待报送记录及其租约凭证。
type ClaimedReport struct {
	ID         uint64          `json:"id"`
	EventID    string          `json:"event_id"`
	ObjectType string          `json:"object_type"`
	ObjectKey  string          `json:"object_key"`
	Data       json.RawMessage `json:"data"`
	LeaseToken string          `json:"lease_token"`
}

// Claim 在一个事务内锁定并领取待报送记录：行锁取代旧实现的乐观租约重查，
// 语义不变——queued/processing 且到期的记录置为 processing 并获得 2 分钟租约。
func (r RegulatoryDispatch) Claim(ctx context.Context, limit int) ([]ClaimedReport, error) {
	if limit <= 0 || limit > 20 {
		return nil, errors.New("claim limit must be between 1 and 20")
	}
	now := time.Now().UTC()
	var pending []struct {
		ID         uint64
		EventID    string
		ObjectType string
		ObjectKey  string
		Payload    string `gorm:"column:payload"`
	}
	tx := r.AdminDB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, tx.Error
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback().Error
		}
	}()
	if err := tx.Raw(`SELECT id,event_id,object_type,object_key,CAST(payload_json AS CHAR) AS payload
		FROM regulatory_report WHERE status IN ('queued','processing') AND next_attempt_at <= ?
		AND (lease_until IS NULL OR lease_until < ?) ORDER BY id LIMIT ? FOR UPDATE`,
		now, now, limit).Scan(&pending).Error; err != nil {
		return nil, err
	}
	claimed := make([]ClaimedReport, 0, len(pending))
	for _, row := range pending {
		token := uuid.NewString()
		if err := tx.Exec(`UPDATE regulatory_report SET status='processing',lease_token=?,lease_until=? WHERE id=?`,
			token, now.Add(2*time.Minute), row.ID).Error; err != nil {
			return nil, err
		}
		claimed = append(claimed, ClaimedReport{
			ID: row.ID, EventID: row.EventID, ObjectType: row.ObjectType,
			ObjectKey: row.ObjectKey, Data: json.RawMessage(row.Payload), LeaseToken: token,
		})
	}
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}
	committed = true
	return claimed, nil
}

// FinishItem 是一笔报送的发送结果；lease_token 不匹配的记录被跳过（租约已失效）。
type FinishItem struct {
	ID         uint64 `json:"id" binding:"required"`
	LeaseToken string `json:"lease_token" binding:"required"`
	Delivered  bool   `json:"delivered"`
	Mode       string `json:"mode"`
	Error      string `json:"error"`
}

// FinishResult 汇总状态推进结果：finished 是推进成功的笔数，lost 是
// 租约已被抢占而跳过的笔数。
type FinishResult struct {
	Finished int `json:"finished"`
	Lost     int `json:"lost"`
}

// Finish 在一个事务内按 lease_token 条件推进状态。成功置 delivered；
// 失败回到 queued 并按有界指数退避（2 的 attempts 次幂秒，封顶 2^11）重排。
func (r RegulatoryDispatch) Finish(ctx context.Context, items []FinishItem) (FinishResult, error) {
	var result FinishResult
	now := time.Now().UTC()
	err := r.AdminDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, item := range items {
			var res *gorm.DB
			if item.Delivered {
				res = tx.Exec(`UPDATE regulatory_report SET status='delivered',attempts=attempts+1,
					delivered_at=?,delivered_mode=?,last_error=NULL,lease_token=NULL,lease_until=NULL WHERE id=? AND lease_token=?`,
					now, item.Mode, item.ID, item.LeaseToken)
			} else {
				// MySQL 按赋值从左到右求值：attempts 先 +1（等于旧值 +1），
				// 退避用 2 的 attempts 次幂秒，与旧 worker 版 retryDelay(attempts+1) 一致。
				res = tx.Exec(`UPDATE regulatory_report SET status='queued',attempts=attempts+1,
					next_attempt_at=DATE_ADD(?, INTERVAL POW(2, LEAST(attempts,11)) SECOND),last_error=?,lease_token=NULL,lease_until=NULL WHERE id=? AND lease_token=?`,
					now, truncateRegulatoryError(item.Error), item.ID, item.LeaseToken)
			}
			if res.Error != nil {
				return res.Error
			}
			if affected := res.RowsAffected; affected == 1 {
				result.Finished++
			} else {
				result.Lost++
			}
		}
		return nil
	})
	return result, err
}

func truncateRegulatoryError(value string) string {
	if len(value) > 512 {
		return value[:512]
	}
	return value
}
