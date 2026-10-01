package snowflake

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"gorm.io/gorm"
)

func snowflakeTestDatabase(t *testing.T) (*sql.DB, *gorm.DB, string) {
	t.Helper()
	url := os.Getenv("TEST_USER_DATABASE_URL")
	if url == "" {
		t.Skip("set disposable test database URL")
	}
	db, err := dbconn.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	orm, err := dbconn.WrapGORM(db)
	if err != nil {
		t.Fatal(err)
	}
	return db, orm, url
}

func TestMySQLAllocationAcrossConcurrentConnectionsAndRestartIntegration(t *testing.T) {
	firstDB, firstORM, url := snowflakeTestDatabase(t)
	secondDB, secondORM, _ := snowflakeTestDatabase(t)
	pools := []*sql.DB{firstDB, secondDB}
	orms := []*gorm.DB{firstORM, secondORM}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const workers, perWorker = 16, 4
	ids := make(chan uint64, workers*perWorker)
	errorsFound := make(chan error, workers)
	start := make(chan struct{})
	var workersDone sync.WaitGroup
	for worker := range workers {
		workersDone.Go(func() {
			<-start
			for iteration := range perWorker {
				pool := (worker + iteration/2) % len(pools)
				var id uint64
				var err error
				if (worker+iteration)%2 == 0 {
					err = orms[pool].WithContext(ctx).Transaction(func(tx *gorm.DB) error {
						id, err = Next(tx)
						return err
					})
				} else {
					var tx *sql.Tx
					tx, err = pools[pool].BeginTx(ctx, nil)
					if err == nil {
						id, err = NextSQL(ctx, tx)
						if err == nil {
							err = tx.Commit()
						} else {
							_ = tx.Rollback()
						}
					}
				}
				if err != nil {
					errorsFound <- err
					return
				}
				ids <- id
			}
		})
	}
	close(start)
	workersDone.Wait()
	close(ids)
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	if t.Failed() {
		t.FailNow()
	}
	seen := make(map[uint64]bool, workers*perWorker)
	var maximum uint64
	for id := range ids {
		if seen[id] || id <= (1<<53)-1 || id > (1<<63)-1 || id>>sequenceBits&((1<<nodeBits)-1) != 0 {
			t.Fatalf("duplicate or invalid Snowflake ID %d", id)
		}
		seen[id] = true
		if id > maximum {
			maximum = id
		}
	}
	if len(seen) != workers*perWorker {
		t.Fatalf("allocated %d unique IDs, want %d", len(seen), workers*perWorker)
	}
	// Drop both application pools. The reopened allocator must continue from DB
	// state rather than a process-local timestamp or sequence.
	if err := firstDB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := secondDB.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := dbconn.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	tx, err := reopened.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	id, err := NextSQL(ctx, tx)
	if err != nil || id <= maximum {
		t.Fatalf("reopened allocator did not advance: id=%d previous=%d err=%v", id, maximum, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLAllocationRollbackAndClockRollbackIntegration(t *testing.T) {
	db, orm, _ := snowflakeTestDatabase(t)
	ctx := context.Background()
	rollback := errors.New("discard allocator and business transaction")
	var before state
	var futureMillis uint64
	err := orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw("SELECT id,last_millisecond,sequence FROM snowflake_state WHERE id=1 FOR UPDATE").Scan(&before).Error; err != nil {
			return err
		}
		futureMillis = uint64(time.Now().UnixMilli()-epochMillis) + 3_600_000
		if futureMillis <= before.LastMillisecond {
			futureMillis = before.LastMillisecond + 1
		}
		if err := tx.Exec("UPDATE snowflake_state SET last_millisecond=?,sequence=? WHERE id=1", futureMillis, maxSequence-1).Error; err != nil {
			return err
		}
		first, err := Next(tx)
		if err != nil || first != futureMillis<<(nodeBits+sequenceBits)|maxSequence {
			t.Fatalf("clock rollback did not use persisted time: id=%d err=%v", first, err)
		}
		underlying, ok := tx.Statement.ConnPool.(*sql.Tx)
		if !ok {
			t.Fatal("GORM transaction does not expose its SQL transaction")
		}
		second, err := NextSQL(ctx, underlying)
		if err != nil || second != (futureMillis+1)<<(nodeBits+sequenceBits) {
			t.Fatalf("SQL/GORM sequence overflow did not share state: id=%d err=%v", second, err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	var after state
	if err := db.QueryRowContext(ctx, "SELECT id,last_millisecond,sequence FROM snowflake_state WHERE id=1").Scan(&after.ID, &after.LastMillisecond, &after.Sequence); err != nil {
		t.Fatal(err)
	}
	if after.LastMillisecond < before.LastMillisecond || after.LastMillisecond >= futureMillis {
		t.Fatalf("transaction rollback persisted the future allocation state: before=%+v after=%+v", before, after)
	}
}
