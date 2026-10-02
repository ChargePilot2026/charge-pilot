// Package dbutil 提供跨领域复用的 MySQL/GORM 小工具：
// UTC 月分区锚点与唯一键冲突判定。计费、支付、订单等各家族
// 在同事务内写记录时共享这两个语义，集中维护避免口径漂移。
package dbutil

import (
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// MonthStart 返回当前 UTC 月份的月初零点，用作各表 created_month 的取值。
//
// created_month 是月分区列，同时参与 payment_order 的主键 (id, created_month)
// 与唯一键 uk_order_no (order_no, created_month)。它的契约是"所属月份的第一天"
// （见 migrations 中该列的 COMMENT），不是写入当天：一旦按当天写入，同一 order_no
// 在不同写入路径上会落到不同分区值，唯一键将不再拦截重复，幂等失效。
// 所有写入点必须共用本函数，不得各自推算。
func MonthStart() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// IsMySQLDuplicate 判定错误是否为 MySQL 唯一键冲突（1062），
// 兼容 GORM 的 ErrDuplicatedKey 包装与 go-sql-driver 原始错误。
func IsMySQLDuplicate(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
