// Package snowflake allocates numeric identifiers across central instances.
package snowflake

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IDs use 41 timestamp bits, 10 node bits and 12 sequence bits. The shared
// user database serializes every allocation, so its node is always zero.
// Persisting the timestamp/sequence also prevents reuse after a process restart
// or clock rollback. An allocation commits with the business record using it.
const (
	epochMillis  int64 = 1577836800000 // 2020-01-01T00:00:00Z
	sequenceBits       = 12
	nodeBits           = 10
	maxSequence        = 1<<sequenceBits - 1
	maxMillis          = 1<<41 - 1
)

type state struct {
	ID              uint8  `gorm:"column:id;primaryKey"`
	LastMillisecond uint64 `gorm:"column:last_millisecond"`
	Sequence        uint16 `gorm:"column:sequence"`
}

func (state) TableName() string { return "snowflake_state" }

// Next must run inside the caller's transaction in user_db. The singleton row
// is provisioned by database initialization, rather than created on each call.
func Next(tx *gorm.DB) (uint64, error) {
	var current state
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = 1").Take(&current).Error; err != nil {
		return 0, err
	}
	id, next, err := advance(current, time.Now().UnixMilli())
	if err != nil {
		return 0, err
	}
	if err := tx.Model(&state{}).Where("id = 1").Updates(map[string]any{
		"last_millisecond": next.LastMillisecond, "sequence": next.Sequence,
	}).Error; err != nil {
		return 0, err
	}
	return id, nil
}

// NextSQL uses the same allocator for SQL-based development seed transactions.
func NextSQL(ctx context.Context, tx *sql.Tx) (uint64, error) {
	var current state
	if err := tx.QueryRowContext(ctx, "SELECT last_millisecond, sequence FROM snowflake_state WHERE id=1 FOR UPDATE").Scan(&current.LastMillisecond, &current.Sequence); err != nil {
		return 0, err
	}
	id, next, err := advance(current, time.Now().UnixMilli())
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE snowflake_state SET last_millisecond=?, sequence=? WHERE id=1", next.LastMillisecond, next.Sequence)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func advance(current state, unixMillis int64) (uint64, state, error) {
	if unixMillis <= epochMillis || current.LastMillisecond > maxMillis || current.Sequence > maxSequence {
		return 0, state{}, errors.New("invalid Snowflake clock or state")
	}
	millis := uint64(unixMillis - epochMillis)
	sequence := uint16(0)
	if millis <= current.LastMillisecond {
		millis = current.LastMillisecond
		if current.Sequence == maxSequence {
			millis++
		} else {
			sequence = current.Sequence + 1
		}
	}
	if millis > maxMillis {
		return 0, state{}, errors.New("Snowflake timestamp exhausted")
	}
	next := state{ID: 1, LastMillisecond: millis, Sequence: sequence}
	return millis<<(nodeBits+sequenceBits) | uint64(sequence), next, nil
}
