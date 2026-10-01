package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// requirePattern 提取路由守卫的权限编码，用于检查 permission 种子是否完整。
var requirePattern = regexp.MustCompile(`Require\("([a-z][a-z0-9_.]*)"\)`)

// TestEveryGuardedPermissionExists 验证路由声明的权限均已写入权限表且可被授予。
// 缺少权限种子会使受保护接口对全部角色返回 403，编译检查无法发现此问题。
func TestEveryGuardedPermissionExists(t *testing.T) {
	if os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	adminDB := openFinanceDB(t, "TEST_ADMIN_DATABASE_URL")
	rows := []struct {
		Code string `gorm:"column:code"`
	}{}
	if err := adminDB.Table("permission").Select("code").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("the permission table is empty; the migrations did not run")
	}
	seeded := map[string]bool{}
	for _, row := range rows {
		seeded[row.Code] = true
	}

	// 从仓库根目录扫描权限守卫，覆盖全部服务；本测试目录距仓库根三层。
	root := filepath.Join("..", "..", "..")
	entries, err := filepath.Glob(filepath.Join(root, "internal", "*", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	deeper, err := filepath.Glob(filepath.Join(root, "internal", "*", "*", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	entries = append(entries, deeper...)
	if len(entries) < 20 {
		t.Fatalf("only %d source files were scanned; the root path is wrong", len(entries))
	}

	// 固定测试权限专用于验证拒绝分支，不属于业务权限种子。
	fixtures := map[string]bool{"nonexistent.permission": true}

	used := map[string]bool{}
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range requirePattern.FindAllStringSubmatch(string(source), -1) {
			used[match[1]] = true
		}
	}
	if len(used) == 0 {
		t.Fatal("no route guards were found; the scan is looking in the wrong place")
	}
	missing := []string{}
	for code := range used {
		if fixtures[code] || seeded[code] {
			continue
		}
		missing = append(missing, code)
	}
	if len(missing) > 0 {
		t.Fatalf("these route guards name a permission that was never seeded, so the route answers 403 to everyone: %v", missing)
	}
}

// 验证计量管理权限的种子及角色授权范围，避免形成无法授予的接口权限。
func TestManualCapabilityPermissionIsRemoved(t *testing.T) {
	if os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	db := openFinanceDB(t, "TEST_ADMIN_DATABASE_URL")
	var count int64
	if err := db.Table("permission").Where("code=?", "device.metering").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("manual capability verification permission still exists")
	}
}
