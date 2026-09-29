package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// AggregateSample is one reading on its way into the rollup tables. It is
// exported so the backfill endpoint, which writes the same table from a
// different package, keeps the rollups in step as well.
type AggregateSample struct {
	DeviceID string
	Port     sql.NullInt16
	Metric   string
	Value    string
	TS       time.Time
}

// refreshAggregates keeps the 15-minute and hourly rollups in step with the raw
// telemetry table.
//
// The raw table keeps every sample, so reading a long window out of it means
// paging through tens of thousands of rows; the curve endpoint has a hard point
// budget and would otherwise return only the newest slice of a 24-hour request
// while presenting it as the whole window. The rollups answer the same question
// in a bounded number of rows.
//
// Both tables carry uk_bucket (device_id, port_no, metric, bucket_start,
// bucket_month), so a plain ON DUPLICATE KEY UPDATE maintains the running mean,
// extremes and sample count in place. The conflict target is spelled out in the
// column list rather than left to the driver, because these tables are
// partitioned on bucket_month and an inferred target produces an empty
// ON DUPLICATE KEY clause.
//
// The rollup is written in the same transaction as the raw rows. If the
// transaction rolls back, neither persists, so a bucket can never claim samples
// that the raw table does not have.
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

// RefreshAggregates is the exported entry point for writers outside this package.
func RefreshAggregates(ctx context.Context, tx *gorm.DB, samples []AggregateSample) error {
	return refreshAggregates(ctx, tx, samples)
}

func upsertAggregate(ctx context.Context, tx *gorm.DB, table string, width time.Duration, samples []AggregateSample) error {
	// One statement per table, not one per sample: the TCP path runs on every
	// device frame and a statement per metric would dominate its cost.
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
