// Command devseed writes and removes the local demo dataset for the charging
// user console (充电用户列表). It exists because the back office is otherwise
// impossible to look at on a fresh machine: user_db ships with no users, and
// every number on that page -- order count, spend, wallet balance -- is zero,
// which hides whether the page is actually reading the right rows.
//
// Everything it writes is tagged with the demo_ prefix on a natural key, and
// -clean removes exactly those rows and nothing else. That is the reason this
// is a Go program rather than a .sql file: user.phone_enc is AES-GCM ciphertext
// and phone_hash is an irreversible digest, so a demo row cannot be produced by
// plain SQL. Only code holding the same key can write a row the C-end login
// would accept.
//
//	go run ./cmd/devseed          # 写入示例数据
//	go run ./cmd/devseed -clean   # 删除示例数据
//
// DSNs come from DATABASE_URL_USER / DATABASE_URL_ADMIN, the same variables the
// services use, and the phone key from PHONE_ENCRYPTION_KEY.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/dbconn"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/phonecrypto"
)

// demoPrefix marks every natural key this program owns. Cleanup matches on it,
// so a partially seeded run and a fully seeded run both clean up completely.
const demoPrefix = "demo_"

// demoStation is the one station the demo orders are placed at. Two-wheel
// charging users only ever charge at a site, so an order without one is not a
// realistic row to develop against.
const (
	demoStationName = demoPrefix + "示例站点-望京SOHO"
	demoStationAddr = "北京市朝阳区望京SOHO T1 楼 B1"
	demoDeviceID    = demoPrefix + "DC589-0001"
	demoDeviceModel = "DC589 8路充电桩"
)

// demoUser is one seeded charging user. Balance and spend are filled in as the
// orders are written, not hard-coded, so the list page's aggregates have to be
// computed correctly to look right.
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

// demoOrder is one seeded charge order, placed daysAgo before its user's
// first sighting. Amounts vary so the cumulative spend column is not a
// suspiciously round number.
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
			// 从未登录过：用来验证列表默认按最后登录倒序时，NULL 排在最后而不是最前。
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
	defer adminDB.Close()

	if *clean {
		remove(ctx, userDB, adminDB)
		return
	}
	seed(ctx, userDB, adminDB)
}

func openDatabases(ctx context.Context) (*sql.DB, *sql.DB, error) {
	userURL := os.Getenv("DATABASE_URL_USER")
	adminURL := os.Getenv("DATABASE_URL_ADMIN")
	if userURL == "" || adminURL == "" {
		return nil, nil, fmt.Errorf("需要设置 DATABASE_URL_USER 与 DATABASE_URL_ADMIN")
	}
	userDB, err := dbconn.Open(ctx, userURL)
	if err != nil {
		return nil, nil, fmt.Errorf("连接 user_db: %w", err)
	}
	adminDB, err := dbconn.Open(ctx, adminURL)
	if err != nil {
		userDB.Close()
		return nil, nil, fmt.Errorf("连接 admin_db: %w", err)
	}
	return userDB, adminDB, nil
}

func seed(ctx context.Context, userDB, adminDB *sql.DB) {
	key := []byte(os.Getenv("PHONE_ENCRYPTION_KEY"))
	if len(key) != phonecrypto.KeySize {
		fail(fmt.Errorf("PHONE_ENCRYPTION_KEY 必须是 %d 字节，当前 %d 字节", phonecrypto.KeySize, len(key)))
	}

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
	for _, u := range demoUsers() {
		firstSeen := now.AddDate(0, 0, -u.daysAgo)
		phoneEnc, phoneHash := []byte(nil), any(nil)
		if u.phone != "" {
			blob, err := phonecrypto.Encrypt(key, u.phone)
			if err != nil {
				fail(fmt.Errorf("加密示例手机号: %w", err))
			}
			phoneEnc, phoneHash = blob, phonecrypto.Hash(u.phone)
		}
		var lastLogin any
		if u.loginDays >= 0 {
			lastLogin = now.AddDate(0, 0, -u.loginDays)
		}
		result, err := userDB.ExecContext(ctx, `
			INSERT INTO user (openid, nickname, phone_enc, phone_hash, gender, status, first_seen_at, last_login_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			u.openid, nullString(u.nickname), phoneEnc, phoneHash, u.gender, u.status, firstSeen, lastLogin, firstSeen)
		if err != nil {
			fail(fmt.Errorf("写入示例用户 %s: %w", u.nickname, err))
		}
		userID, _ := result.LastInsertId()

		if _, err := userDB.ExecContext(ctx, `
			INSERT INTO wallet_account (user_id, balance_cents, frozen_cents, status, created_at)
			VALUES (?, ?, ?, 'active', ?)`,
			userID, u.balance, u.frozen, firstSeen); err != nil {
			fail(fmt.Errorf("写入示例钱包: %w", err))
		}

		orderCount := 0
		for _, o := range u.orders {
			created := now.AddDate(0, 0, -o.daysAgo)
			if err := seedOrder(ctx, userDB, userID, u, o, created, deviceID, stationID); err != nil {
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
		INSERT INTO station (name, address, longitude, latitude, open_hours, contact_phone, status)
		VALUES (?, ?, 116.48100000, 39.99600000, '00:00-24:00', '010-88880000', 'active')`,
		demoStationName, demoStationAddr)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// seedDevice 建示例设备。
//
// reports_energy 必须为 true：示例订单全部是按电量计的（charged_kwh 有值），
// 而计费模板下发时会校验设备是否具备该模式的计量能力——按电量计费的模板
// 会被设备能力门控挡下，理由是"未声明电量上报能力，无法按电量计费"。设备
// 不报电量、订单却按电量计，示例数据集就自相矛盾，连一套正常计费模板都配不上。
func seedDevice(ctx context.Context, adminDB *sql.DB, stationID int64) (string, error) {
	if _, err := adminDB.ExecContext(ctx, `
		INSERT INTO device_meta (device_id, station_id, model, serial_no, status, charge_mode, reports_energy, install_at)
		VALUES (?, ?, ?, ?, 'enabled', 'server_energy', 1, NOW(3))`,
		demoDeviceID, stationID, demoDeviceModel, demoDeviceID); err != nil {
		return "", err
	}
	return demoDeviceID, nil
}

func seedOrder(ctx context.Context, userDB *sql.DB, userID int64, u demoUser, o demoOrder, created time.Time,
	deviceID string, stationID int64) error {

	var total any
	if o.cents > 0 {
		total = o.cents
	}
	var electric, service any
	if o.cents > 0 {
		// 电费占七成、服务费占三成，合计等于 total_cents，避免账单页面对不上。
		electric = o.cents * 7 / 10
		service = o.cents - o.cents*7/10
	}
	// charge_order 是按 created_month 做 RANGE 分区的，created_month 是分区键且
	// 没有默认值，必须显式写入；写成订单创建时间的月初。
	month := time.Date(created.Year(), created.Month(), 1, 0, 0, 0, 0, time.UTC)
	orderNo := fmt.Sprintf("DEMO-%s-%d", month.Format("200601"), userID*100+int64(o.daysAgo))
	result, err := userDB.ExecContext(ctx, `
		INSERT INTO charge_order
		  (order_no, user_id, device_id, port_no, status, started_at, charged_kwh, charged_seconds,
		   electric_cents, service_cents, total_cents, created_month, created_at)
		VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		orderNo, userID, deviceID, o.status, created, o.kwh, 3600, electric, service, total, month.Format("2006-01-02"), created)
	if err != nil {
		return err
	}
	orderID, _ := result.LastInsertId()

	// charge_payment_intent 是订单与站点之间的唯一桥梁：charge_order 本身不存
	// station_id，后台查订单所属站点全靠这张表。不写它，订单档案里的站点会是空。
	//
	// payment_order_id 上有唯一键，示例数据不真的建 payment_order 行，所以按订单
	// 主键取一个互不相同的合成值——都填 0 会直接违反 uk_payment_order。
	_, err = userDB.ExecContext(ctx, `
		INSERT INTO charge_payment_intent
		  (intent_id, client_request_id, merchant_order_no, payment_order_id, user_id, openid, device_id,
		   port_no, port_code, station_id, pricing_rule_id, pricing_rule_version, pricing_snapshot,
		   estimated_kwh, estimated_minutes, electric_cents, service_cents, total_cents, discount_cents,
		   charge_mode, charge_quantity, status, expires_at, paid_at, created_at, charge_order_id)
		VALUES (UUID(), UUID(), ?, ?, ?, ?, ?, 1, ?, ?, 0, 0, JSON_OBJECT('source', 'devseed'),
		        ?, 60, ?, ?, ?, 0, 0, 1, ?, ?, ?, ?, ?)`,
		fmt.Sprintf("DEMO-MERCHANT-%d", orderID), orderID, userID, u.openid, deviceID,
		fmt.Sprintf("%s-01", deviceID), stationID, o.kwh, orZero(electric), orZero(service), orZero(total),
		intentStatus(o.status), created.Add(15*time.Minute), created, created, orderID)
	return err
}

// intentStatus 把订单状态映射成支付意图状态。已取消的订单对应的意图是 closed，
// 其余一律按已支付处理——示例数据里不存在"下单了但没付钱"的半截状态。
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
	// 删除顺序与插入相反：先摘掉依赖订单的支付意图和报障，再删订单，最后删用户。
	// 报障按设备号删、其余按 openid 前缀删——报障表的 user_id 可空，按用户删会漏掉
	// user_id 为 NULL 的行。
	statements := []struct {
		query string
		arg   any
	}{
		{"DELETE FROM charge_payment_intent WHERE openid LIKE ?", demoPrefix + "%"},
		{"DELETE FROM device_fault_report WHERE device_id = ?", demoDeviceID},
		{"DELETE FROM charge_order WHERE order_no LIKE ?", "DEMO-%"},
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
	if _, err := adminDB.ExecContext(ctx, "DELETE FROM device_meta WHERE device_id = ?", demoDeviceID); err != nil {
		fail(fmt.Errorf("清理示例设备: %w", err))
	}
	if _, err := adminDB.ExecContext(ctx, "DELETE FROM station WHERE name = ?", demoStationName); err != nil {
		fail(fmt.Errorf("清理示例站点: %w", err))
	}
	fmt.Println("示例数据已清理")
}

// faultType 让报障单的类型有变化，避免示例数据看起来像同一条记录复制了三次。
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
