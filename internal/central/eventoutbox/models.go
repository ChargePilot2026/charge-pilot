// Package eventoutbox 承载 event_outbox 表记录类型。
// event_outbox 由 order / payment / settlement 多个家族在同一事务内共写，
// 不隶属任何单一家族，故独立成包避免反向依赖。
package eventoutbox

type EventOutboxRecord struct {
	ID           uint64 `gorm:"column:id;primaryKey"`
	EventID      string `gorm:"column:event_id"`
	Stream       string `gorm:"column:stream"`
	EnvelopeJSON []byte `gorm:"column:envelope_json"`
}

func (EventOutboxRecord) TableName() string { return "event_outbox" }
