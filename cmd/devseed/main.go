// Command devseed 创建或清理充电用户控制台的本地示例数据。
// 仅处理 openid 或设备编号带 demo_ 前缀的记录；-clean 用于清理。
// 数据库连接取自 DATABASE_URL_CENTRAL，用户、订单和钱包数据按业务关系生成。
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/snowflake"
)

// demoPrefix 标识本工具创建的记录，支持清理完整或部分写入的示例数据。
const demoPrefix = "demo_"

// demoStation 是示例订单统一关联的站点，用于验证站点维度的查询与统计。
const (
	demoStationName = demoPrefix + "示例站点-望京SOHO"
	demoStationAddr = "北京市朝阳区望京SOHO T1 楼 B1"
	demoDeviceID    = demoPrefix + "DC589-0001"
	demoDeviceModel = "DC589 8路充电桩"
)

// demoUser 描述示例用户；消费统计随订单生成，钱包余额用于列表与详情验证。
type demoUser struct {
	openid    string
	nickname  string
	phone     string // 空串表示不绑手机号
	gender    string
	status    string
	balance   int64
	frozen    int64
	faults    int
	daysAgo   int // 首次出现距今天数
	loginDays int // 最后登录距今天数；-1 表示从未登录
	orders    []demoOrder
}

// demoOrder 描述示例订单的状态、金额及距当前日期的天数。
type demoOrder struct {
	daysAgo int
	status  string
	kwh     string
	cents   int64
}

func demoUsers() []demoUser {
	return []demoUser{
		{
			openid: demoPrefix + "openid_a1b2c3", nickname: "张伟", phone: "13800138001",
			gender: "male", status: "active", balance: 4260, loginDays: 0,
			faults: 1, daysAgo: 96,
			orders: []demoOrder{
				{daysAgo: 94, status: "completed", kwh: "18.4000", cents: 1588},
				{daysAgo: 61, status: "completed", kwh: "26.1500", cents: 2242},
				{daysAgo: 30, status: "completed", kwh: "12.8000", cents: 1105},
				{daysAgo: 6, status: "completed", kwh: "31.2000", cents: 2688},
			},
		},
		{
			openid: demoPrefix + "openid_d4e5f6", nickname: "李娜", phone: "13900139002",
			gender: "female", status: "active", balance: 0, loginDays: 1,
			daysAgo: 74,
			orders: []demoOrder{
				{daysAgo: 70, status: "completed", kwh: "9.6000", cents: 826},
				{daysAgo: 44, status: "completed", kwh: "22.4000", cents: 1929},
				{daysAgo: 12, status: "completed", kwh: "15.0000", cents: 1290},
			},
		},
		{
			// 验证未登录用户的 last_login_at 为 NULL，并在倒序列表中排在末尾。
			openid: demoPrefix + "openid_g7h8i9", nickname: "", phone: "13700137003",
			gender: "unknown", status: "active", loginDays: -1, daysAgo: 40,
			orders: []demoOrder{
				{daysAgo: 39, status: "cancelled", kwh: "0.0000", cents: 0},
			},
		},
		{
			openid: demoPrefix + "openid_j1k2l3", nickname: "王强", phone: "13600136004",
			gender: "male", status: "frozen", balance: 128, frozen: 128, loginDays: 22,
			faults: 3, daysAgo: 120,
			orders: []demoOrder{
				{daysAgo: 118, status: "completed", kwh: "28.9000", cents: 2489},
				{daysAgo: 88, status: "completed", kwh: "19.3000", cents: 1664},
				{daysAgo: 47, status: "completed", kwh: "34.1000", cents: 2931},
				{daysAgo: 23, status: "refunded", kwh: "11.2000", cents: 964},
			},
		},
		{
			openid: demoPrefix + "openid_m4n5o6", nickname: "陈静", phone: "13500135005",
			gender: "female", status: "active", balance: 8890, loginDays: 3,
			daysAgo: 58,
			orders: []demoOrder{
				{daysAgo: 55, status: "completed", kwh: "16.7000", cents: 1436},
				{daysAgo: 33, status: "completed", kwh: "24.3000", cents: 2088},
				{daysAgo: 9, status: "completed", kwh: "13.5000", cents: 1161},
				{daysAgo: 2, status: "charging", kwh: "6.4000", cents: 0},
			},
		},
		{
			// 未绑手机号：验证列表里"未绑定"的展示，以及手机号搜索查不到这一行。
			openid: demoPrefix + "openid_p7q8r9", nickname: "刘洋", phone: "",
			gender: "unknown", status: "active", balance: 620, loginDays: 9,
			daysAgo: 31,
			orders: []demoOrder{
				{daysAgo: 28, status: "completed", kwh: "20.1000", cents: 1728},
				{daysAgo: 5, status: "completed", kwh: "17.6000", cents: 1512},
			},
		},
		{
			openid: demoPrefix + "openid_s1t2u3", nickname: "赵敏", phone: "13400134007",
			gender: "female", status: "active", balance: 2450, loginDays: 0,
			daysAgo: 15,
			orders: []demoOrder{
				{daysAgo: 14, status: "completed", kwh: "14.6000", cents: 1254},
				{daysAgo: 4, status: "completed", kwh: "21.8000", cents: 1873},
			},
		},
		{
			openid: demoPrefix + "openid_v4w5x6", nickname: "", phone: "13300133008",
			gender: "unknown", status: "active", balance: 0, loginDays: 46,
			daysAgo: 52,
		},
	}
}

func main() {
	clean := flag.Bool("clean", false, "删除示例数据后退出")
	flag.Parse()

	ctx := context.Background()
	userDB, adminDB, err := openDatabases(ctx)
	if err != nil {
		fail(err)
	}
	defer userDB.Close()

	if *clean {
		remove(ctx, userDB, adminDB)
		return
	}
	seed(ctx, userDB, adminDB)
}

func openDatabases(ctx context.Context) (*sql.DB, *sql.DB, error) {
	db, err := dbconn.Open(ctx, os.Getenv("DATABASE_URL_CENTRAL"))
	return db, db, err
}

func seed(ctx context.Context, userDB, adminDB *sql.DB) {
	remove(ctx, userDB, adminDB)

	stationID, err := seedStation(ctx, adminDB)
	if err != nil {
		fail(fmt.Errorf("写入示例站点: %w", err))
	}
	deviceID, err := seedDevice(ctx, adminDB, stationID)
	if err != nil {
		fail(fmt.Errorf("写入示例设备: %w", err))
	}

	now := time.Now().UTC()
	totalUsers, totalOrders := 0, 0
	ruleID, err := seedStationScheme(ctx, adminDB, stationID)
	if err != nil {
		fail(fmt.Errorf("写入完整方案: %w", err))
	}

	for userIndex, u := range demoUsers() {
		firstSeen := now.AddDate(0, 0, -u.daysAgo)
		var lastLogin any
		if u.loginDays >= 0 {
			lastLogin = now.AddDate(0, 0, -u.loginDays)
		}
		var userID int64
		err := withSeedTransaction(ctx, userDB, func(tx *sql.Tx) error {
			id, err := snowflake.NextSQL(ctx, tx)
			if err != nil {
				return err
			}
			userID = int64(id)
			_, err = tx.ExecContext(ctx, `
				INSERT INTO user (id, openid, nickname, phone, gender, status, first_seen_at, last_login_at, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				userID, u.openid, nullString(u.nickname), nullString(u.phone), u.gender, u.status, firstSeen, lastLogin, firstSeen)
			return err
		})
		if err != nil {
			fail(fmt.Errorf("写入示例用户 %s: %w", u.nickname, err))
		}

		if _, err := userDB.ExecContext(ctx, `
			INSERT INTO wallet_account (user_id, balance_cents, frozen_cents, status, created_at)
			VALUES (?, ?, ?, 'active', ?)`,
			userID, u.balance, u.frozen, firstSeen); err != nil {
			fail(fmt.Errorf("写入示例钱包: %w", err))
		}

		orderCount := 0
		for _, o := range u.orders {
			created := now.AddDate(0, 0, -o.daysAgo).Add(-time.Duration(userIndex) * time.Second)
			if err := seedOrder(ctx, userDB, userID, u, o, created, deviceID, stationID, ruleID); err != nil {
				fail(fmt.Errorf("写入示例订单: %w", err))
			}
			orderCount++
		}
		for i := 0; i < u.faults; i++ {
			if _, err := userDB.ExecContext(ctx, `
				INSERT INTO device_fault_report (device_id, user_id, report_source, fault_type, description, status, created_at)
				VALUES (?, ?, 'user', ?, ?, ?, ?)`,
				deviceID, userID, faultType(i), fmt.Sprintf("示例报障单 #%d：扫码后无法启动充电", i+1),
				faultStatus(i), now.AddDate(0, 0, -u.daysAgo+i+1)); err != nil {
				fail(fmt.Errorf("写入示例报障: %w", err))
			}
		}
		totalUsers++
		totalOrders += orderCount
	}

	fmt.Printf("示例数据写入完成：%d 位充电用户、%d 笔订单、1 个站点、1 台设备\n", totalUsers, totalOrders)
	fmt.Printf("清理命令：go run ./cmd/devseed -clean\n")
}

func seedStation(ctx context.Context, adminDB *sql.DB) (int64, error) {
	result, err := adminDB.ExecContext(ctx, `
		INSERT INTO station (name, address, longitude, latitude, contact_phone, status)
		VALUES (?, ?, 116.48100000, 39.99600000, '010-88880000', 'active')`,
		demoStationName, demoStationAddr)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// seedDevice 创建示例设备，并根据所选协议确定设备能力。
func seedDevice(ctx context.Context, adminDB *sql.DB, stationID int64) (string, error) {
	if _, err := adminDB.ExecContext(ctx, `
		INSERT INTO device_meta (device_id, station_id, model, serial_no, status, charge_mode, protocol_adapter)
		VALUES (?, ?, ?, ?, 'enabled', 'server_energy', 'dc589')`,
		demoDeviceID, stationID, demoDeviceModel, demoDeviceID); err != nil {
		return "", err
	}
	return demoDeviceID, nil
}

func seedOrder(ctx context.Context, userDB *sql.DB, userID int64, u demoUser, o demoOrder, created time.Time,
	deviceID string, stationID, ruleID int64) error {

	var total any
	if o.cents > 0 {
		total = o.cents
	}
	var electric, service any
	if o.cents > 0 {
		// Historical demo orders use fixed duration packages: the package fee is service.
		electric = 0
		service = o.cents
	}
	// created_month 是订单表的必填 RANGE 分区键，取订单创建时间所在月份的月初。
	month := time.Date(created.Year(), created.Month(), 1, 0, 0, 0, 0, time.UTC)
	orderNo := "C" + created.In(time.FixedZone("CST", 8*3600)).Format("20060102150405") + deviceID + "01"
	// 已结束的示例订单按 charged_seconds 推算 ended_at，保持时长与结束时间一致。
	// 该时间用于订单展示及按结束日期统计的看板；未结束的订单保持 NULL。
	var endedAt any
	if orderHasEndedAt(o.status) {
		endedAt = created.Add(time.Duration(3600) * time.Second)
	}
	result, err := userDB.ExecContext(ctx, `
		INSERT INTO charge_order
		  (order_no, user_id, device_id, port_no, status, started_at, ended_at, charged_kwh, charged_seconds,
		   electric_cents, service_cents, total_cents, created_month, created_at)
		VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		orderNo, userID, deviceID, o.status, created, endedAt, o.kwh, 3600, electric, service, total, month.Format("2006-01-02"), created)
	if err != nil {
		return err
	}
	orderID, _ := result.LastInsertId()

	return seedOrderContract(ctx, userDB, orderID, userID, u, o, created, deviceID, stationID, ruleID, orderNo)
}

// orderHasEndedAt 判断示例订单是否已结束；充电中或取消的示例订单不设置结束时间。
func orderHasEndedAt(status string) bool {
	return status == "completed" || status == "refunded"
}

// intentStatus 将取消订单映射为 closed；其他示例订单均使用已支付的意图状态。
func intentStatus(orderStatus string) string {
	if orderStatus == "cancelled" {
		return "closed"
	}
	return "paid"
}

func remove(ctx context.Context, userDB, adminDB *sql.DB) {
	tx, err := userDB.BeginTx(ctx, nil)
	if err != nil {
		fail(fmt.Errorf("开始清理事务: %w", err))
	}
	// 按依赖顺序清理支付意图、报障、订单和用户。
	// 报障按设备编号匹配，避免遗漏 user_id 为 NULL 的示例记录。
	statements := []struct {
		query string
		arg   any
	}{
		{"DELETE FROM charge_fee_receipt WHERE charge_order_id IN (SELECT id FROM charge_order WHERE device_id = ?)", demoDeviceID},
		{"DELETE FROM charge_order_pricing WHERE charge_order_id IN (SELECT id FROM charge_order WHERE device_id = ?)", demoDeviceID},
		{"DELETE FROM payment_order WHERE user_id IN (SELECT id FROM user WHERE openid LIKE ?)", demoPrefix + "%"},
		{"DELETE FROM charge_payment_intent WHERE openid LIKE ?", demoPrefix + "%"},
		{"DELETE FROM device_fault_report WHERE device_id = ?", demoDeviceID},
		{"DELETE FROM charge_order WHERE device_id = ?", demoDeviceID},
		{"DELETE FROM coupon_grant WHERE user_id IN (SELECT id FROM user WHERE openid LIKE ?)", demoPrefix + "%"},
		{"DELETE FROM wallet_account WHERE user_id IN (SELECT id FROM user WHERE openid LIKE ?)", demoPrefix + "%"},
		{"DELETE FROM user WHERE openid LIKE ?", demoPrefix + "%"},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.arg); err != nil {
			tx.Rollback()
			fail(fmt.Errorf("清理示例数据: %w", err))
		}
	}
	if err := tx.Commit(); err != nil {
		fail(fmt.Errorf("提交清理事务: %w", err))
	}
	if _, err := adminDB.ExecContext(ctx, "DELETE FROM pricing_rule WHERE station_id IN (SELECT id FROM station WHERE name=?)", demoStationName); err != nil {
		fail(err)
	}
	if _, err := adminDB.ExecContext(ctx, "DELETE FROM pricing_template WHERE name=?", demoScheme().Name); err != nil {
		fail(err)
	}
	if _, err := adminDB.ExecContext(ctx, "DELETE FROM device_meta WHERE device_id = ?", demoDeviceID); err != nil {
		fail(fmt.Errorf("清理示例设备: %w", err))
	}
	if _, err := adminDB.ExecContext(ctx, "DELETE FROM station WHERE name = ?", demoStationName); err != nil {
		fail(fmt.Errorf("清理示例站点: %w", err))
	}
	fmt.Println("示例数据已清理")
}

// faultType 为示例报障分配不同类型，覆盖列表分类展示。
func faultType(i int) string {
	return []string{"mechanical", "communication", "electrical"}[i%3]
}

func faultStatus(i int) string {
	return []string{"open", "dispatched", "fixed"}[i%3]
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func orZero(value any) any {
	if value == nil {
		return 0
	}
	return value
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "devseed:", err)
	os.Exit(1)
}
