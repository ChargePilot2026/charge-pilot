package admin

import "testing"

// TestNormalizeRowsTurnsTinyintFlagsIntoBooleans 锁住一件事：MySQL 的 tinyint(1)
// 回来是数字，前端拿到的必须是 true/false 而不是 1/0。
//
// 这不是洁癖。前端设备矩阵用 `reports_energy === true` 判设备能不能按电量计费，
// 而设备矩阵的列直接来自 device_meta。数字 1 会被判成 false，一台明明上报电量的
// 桩在界面上显示成"仅时长"——运营照着这个结论去分配计费方式，真正下发时才会被
// "未声明电量上报能力"挡回来，理由还指向一台无辜的设备。
func TestNormalizeRowsTurnsTinyintFlagsIntoBooleans(t *testing.T) {
	rows := []map[string]any{{
		"device_id":               "demo_DC589-0001",
		"reports_energy":          int64(1),
		"reports_segmented_power": int64(0),
		"enabled":                 int64(1),
		"station_id":              int64(14),
		"status":                  "active",
	}}
	normalizeRows(rows)

	row := rows[0]
	for _, name := range []string{"reports_energy", "enabled"} {
		if v, ok := row[name].(bool); !ok || !v {
			t.Errorf("%s = %#v，期望 true", name, row[name])
		}
	}
	if v, ok := row["reports_segmented_power"].(bool); !ok || v {
		t.Errorf("reports_segmented_power = %#v，期望 false", row["reports_segmented_power"])
	}
	// 非布尔列不能被顺手改掉：station_id 还是数字，status 还是字符串。
	if _, ok := row["station_id"].(int64); !ok {
		t.Errorf("station_id 被改成了 %#v，不该动", row["station_id"])
	}
	if row["status"] != "active" {
		t.Errorf("status = %#v，不该动", row["status"])
	}
}
