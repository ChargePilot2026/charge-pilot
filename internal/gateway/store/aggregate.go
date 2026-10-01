package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// AggregateSample 表示聚合读数，供实时遥测和断线补传共同维护汇总表。
type AggregateSample struct {
	DeviceID string
	Port     sql.NullInt16
	Metric   string
	Value    string
	TS       time.Time
}

// refreshAggregates 在原始遥测事务内维护 15 分钟和小时汇总，支持有界的长窗口查询。
// 桶唯一键使用 device_id、port_key、metric、bucket_start 和 bucket_month；port_key 统一 NULL 端口。
// 显式指定冲突更新字段，累加样本数并更新加权均值、最小值和最大值。
func refreshAggregates(ctx context.Context, tx *gorm.DB, samples []AggregateSample) error {
	if len(samples) == 0 {
		return nil
	}
	for _, spec := range []struct {
		table string
		width time.Duration
	}{{"telemetry_aggregate_15min", 15 * time.Minute}, {"telemetry_aggregate_hourly", time.Hour}} {
		if err := upsertAggregate(ctx, tx, spec.table, spec.width, samples); err != nil {
			return err
		}
	}
	return nil
}

// RefreshAggregates 是给本包之外的写入方用的导出入口。
func RefreshAggregates(ctx context.Context, tx *gorm.DB, samples []AggregateSample) error {
	return refreshAggregates(ctx, tx, samples)
}

func upsertAggregate(ctx context.Context, tx *gorm.DB, table string, width time.Duration, samples []AggregateSample) error {
	// 每种汇总粒度批量执行一条 SQL，避免 TCP 写入路径按指标逐条访问数据库。
	values := make([]string, 0, len(samples))
	args := make([]any, 0, len(samples)*9)
	for _, sample := range samples {
		bucket := sample.TS.UTC().Truncate(width)
		port := any(nil)
		if sample.Port.Valid {
			port = int16(sample.Port.Int16)
		}
		month := time.Date(bucket.Year(), bucket.Month(), 1, 0, 0, 0, 0, time.UTC)
		values = append(values, "(?,?,?,?,?,?,?,?,?)")
		args = append(args, sample.DeviceID, port, sample.Metric, bucket, month, sample.Value, sample.Value, sample.Value, 1)
	}
	statement := fmt.Sprintf(`INSERT INTO %s
		(device_id, port_no, metric, bucket_start, bucket_month, avg_value, min_value, max_value, count)
		VALUES %s
		ON DUPLICATE KEY UPDATE
		avg_value = (avg_value * count + VALUES(avg_value) * VALUES(count)) / (count + VALUES(count)),
		min_value = LEAST(min_value, VALUES(min_value)),
		max_value = GREATEST(max_value, VALUES(max_value)),
		count = count + VALUES(count)`, table, strings.Join(values, ","))
	return tx.WithContext(ctx).Exec(statement, args...).Error
}
