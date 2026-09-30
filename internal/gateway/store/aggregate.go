package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// AggregateSample 是一条正在写往汇总表的读数。它被导出，
// 是为了让断线补传这个从另一个包写同一张表的接口
// 也能同步维护汇总表。
type AggregateSample struct {
	DeviceID string
	Port     sql.NullInt16
	Metric   string
	Value    string
	TS       time.Time
}

// refreshAggregates 让 15 分钟与小时两张汇总表跟上原始 telemetry 表。
//
// 原始表保留每一个样本，所以从它里面读一个长窗口意味着
// 要翻过几万行；曲线接口的点数预算是硬的，
// 否则它只会把 24 小时请求里最新的一段返回来，
// 却对外声称这就是整个窗口。
// 汇总表用有界的行数回答同样的问题。
//
// 两张表都带 uk_bucket（device_id， port_no， metric， bucket_start，
// bucket_month），所以一句普通的 ON DUPLICATE KEY UPDATE 就能就地
// 维护运行均值、极值和样本数。冲突目标写死在列清单里而不交给驱动去推断，
// 因为这些表是按 bucket_month 分区的，
// 推断出来的目标会生成一句空的 ON DUPLICATE KEY 子句。
//
// 汇总表与原始行写在同一个事务里。事务一旦回滚，两者都不会落盘，
// 所以任何一个桶都不可能声称拥有原始表里并不存在的样本。
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
	// 每张表一条语句，而不是每个样本一条：TCP 那条路每来一帧都会跑一次，
	// 一个指标一条语句的话成本会被它占满。
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
