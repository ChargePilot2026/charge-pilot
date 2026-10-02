package dbutil

import (
	"errors"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func TestMonthStartIsFirstDayOfCurrentUTCMonth(t *testing.T) {
	got := MonthStart()
	if got.Day() != 1 {
		t.Fatalf("created_month anchor day = %d, want 1 (writing the write day breaks uk_order_no)", got.Day())
	}
	if h, m, s := got.Clock(); h != 0 || m != 0 || s != 0 {
		t.Fatalf("anchor clock = %02d:%02d:%02d, want 00:00:00", h, m, s)
	}
	if got.Location() != time.UTC {
		t.Fatalf("anchor location = %v, want UTC", got.Location())
	}
	now := time.Now().UTC()
	if got.Year() != now.Year() || got.Month() != now.Month() {
		t.Fatalf("anchor = %v, want %04d-%02d", got, now.Year(), now.Month())
	}
}

// 同一分钟内多次取值必须完全一致，否则同一事务内写入两条记录会落进不同分区。
func TestMonthStartIsStableWithinAMonth(t *testing.T) {
	first := MonthStart()
	second := MonthStart()
	if !first.Equal(second) {
		t.Fatalf("anchor drifted between calls: %v != %v", first, second)
	}
}

// 锚点必须落在 RANGE 分区覆盖的区间内：月首 to_days() 等于该月分区的下界。
// 2026-10-01 的 to_days 为 740255，正是 p_2026m10 的 VALUES LESS THAN 起点。
func TestMonthStartLandsOnPartitionBoundary(t *testing.T) {
	monthStart := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	nextMonth := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if !monthStart.Before(nextMonth) {
		t.Fatal("month start is not before the next month start")
	}
	// 同月任意一天都必须落在 [月首, 次月首) 区间，从而归入同一个分区。
	for day := 1; day <= 31; day++ {
		at := time.Date(2026, 10, day, 12, 0, 0, 0, time.UTC)
		if at.Before(monthStart) || !at.Before(nextMonth) {
			t.Fatalf("2026-10-%02d escapes its month partition", day)
		}
	}
}

func TestIsMySQLDuplicate(t *testing.T) {
	if IsMySQLDuplicate(nil) {
		t.Fatal("nil error reported as duplicate")
	}
	if IsMySQLDuplicate(errors.New("connection reset")) {
		t.Fatal("unrelated error reported as duplicate")
	}
	if !IsMySQLDuplicate(gorm.ErrDuplicatedKey) {
		t.Fatal("gorm.ErrDuplicatedKey not recognised")
	}
	if !IsMySQLDuplicate(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}) {
		t.Fatal("raw 1062 not recognised")
	}
	if IsMySQLDuplicate(&mysql.MySQLError{Number: 1146, Message: "Table doesn't exist"}) {
		t.Fatal("non-duplicate MySQL error reported as duplicate")
	}
	// 必须穿透包装，否则 fmt.Errorf("%w") 之后的错误会漏判。
	wrapped := errors.Join(errors.New("settle refund"), &mysql.MySQLError{Number: 1062})
	if !IsMySQLDuplicate(wrapped) {
		t.Fatal("wrapped 1062 not recognised")
	}
}
