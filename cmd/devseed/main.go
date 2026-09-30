// Command devseed 为充电用户控制台（充电用户列表）写入与清除本地示例数据。
// 它的存在是因为在一台全新机器上后台根本没法看：user_db 出厂没有用户，
// 那个页面上的每个数字——订单数、消费额、钱包余额——都是 0，
// 这会掩盖页面到底有没有读到正确的行。
//
// 它写入的每一行都在自然键上带 demo_ 前缀，-clean 只删这些行，不多不少。
// 这也是它做成 Go 程序而不是 .sql 文件的原因：user.phone_enc 是 AES-GCM 密文、
// phone_hash 是不可逆摘要，示例行没法用纯 SQL 造出来。
// 只有持有同一把密钥的代码，才能写出一行 C 端登录会认的记录。
//
//	go run ./cmd/devseed          # 写入示例数据
//	go run ./cmd/devseed -clean   # 删除示例数据
//
// DSN 取自 DATABASE_URL_USER / DATABASE_URL_ADMIN（与服务用的是同一组变量），
// 手机号密钥取自 PHONE_ENCRYPTION_KEY。
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

// demoPrefix 标记了本程序拥有的每一个自然键。清理就是按它匹配的，
// 所以播种了一半和播种完整，两种情况都能清干净。
const demoPrefix = "demo_"

// demoStation 是示例订单所在的唯一站点。两轮车只会在站点充电，
// 没有站点的订单不是一条能拿来做开发的真实数据。
const (
	demoStationName = demoPrefix + "示例站点-望京SOHO"
	demoStationAddr = "北京市朝阳区望京SOHO T1 楼 B1"
	demoDeviceID    = demoPrefix + "DC589-0001"
	demoDeviceModel = "DC589 8路充电桩"
)

// demoUser 是一个示例充电用户。余额与消费额是随着订单写入时顺带算出来的，
// 不是写死的数值，所以列表页的汇总是真的算对了才显示得对。
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

// demoOrder 是一笔示例充电订单，下单时间比它所属用户首次出现早 daysAgo 天。
// 金额各不相同，免得累计消费那列看起来像是个可疑的整数。
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
	ruleID, err := seedStationScheme(ctx, adminDB, stationID)
	if err != nil {
		fail(fmt.Errorf("写入完整方案: %w", err))
	}

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

// seedDevice 建示例设备。
//
// Device abilities follow its selected protocol.
func seedDevice(ctx context.Context, adminDB *sql.DB, stationID int64) (string, error) {
	if _, err := adminDB.ExecContext(ctx, `
		INSERT INTO device_meta (device_id, station_id, model, serial_no, status, charge_mode, protocol_adapter, install_at)
		VALUES (?, ?, ?, ?, 'enabled', 'server_energy', 'dc589', NOW(3))`,
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
	// charge_order 是按 created_month 做 RANGE 分区的，created_month 是分区键且
	// 没有默认值，必须显式写入；写成订单创建时间的月初。
	month := time.Date(created.Year(), created.Month(), 1, 0, 0, 0, 0, time.UTC)
	orderNo := fmt.Sprintf("DEMO-%s-%d", month.Format("200601"), userID*100+int64(o.daysAgo))
	// 已结束的订单必须写 ended_at。结算和首页都靠它：后台首页的"近 7 天完成
	// 订单与结算金额"是按 ended_at 分天汇总的，示例数据一律留空的话，运营打开
	// 后台第一眼看到的就是一张全 0 的表，还以为平台没数据。订单列表的"结束时间"
	// 列同样因此全是"—"。
	//
	// 结束时间按 charged_seconds 往后推，不另造时长：示例订单的时长本来就统一
	// 取 3600 秒，这里只是把这个口径落到 ended_at 上，两处不再各说各话。
	// 仍在充电的订单不写——正在充的订单本来就还没有结束时刻。
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

// orderHasEndedAt 报告这个状态的订单是否应当带结束时刻。
//
// 只有真正跑完的订单才有结束时刻：仍在充电的订单没有，已取消的订单是被中止
// 的、同样没有。示例数据的 status 只用这四种，所以列全即可。
func orderHasEndedAt(status string) bool {
	return status == "completed" || status == "refunded"
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
		{"DELETE FROM charge_fee_receipt WHERE charge_order_id IN (SELECT id FROM charge_order WHERE order_no LIKE ?)", "DEMO-%"},
		{"DELETE FROM charge_order_pricing WHERE charge_order_id IN (SELECT id FROM charge_order WHERE order_no LIKE ?)", "DEMO-%"},
		{"DELETE FROM payment_order WHERE order_no LIKE ?", "DEMO-MERCHANT-%"},
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
