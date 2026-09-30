package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// requirePattern 匹配路由守卫传进去的权限参数。这个测试的全部目的
// 就是抓住"代码里写了权限名、却从没往 permission 表里种下去"的权限，
// 而这种字符串唯一会出现的地方
// 就是源码本身。
var requirePattern = regexp.MustCompile(`Require\("([a-z][a-z0-9_.]*)"\)`)

// TestEveryGuardedPermissionExists 守的是一种别的测试都看不见的故障模式：
// 一条挂在"从没写进 permission 表"的权限码后面的路由，
// 既不是编译错误，也不是单元测试失败，
// 它是一个对所有人永远静默返回 403 的接口。
//
// 这里真的发生过。新加的一条路由要求 "device.update"，
// 而这个权限并不存在——系统里只有 device.read 和 device.import，
// 没有第三个 device 权限——于是任何角色都调不了这个接口，
// 超级管理员也不行，因为 Require 没有旁路。
// 之所以一直没人发现，是因为这个接口从没被调用过。
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

	// 从仓库根目录开始扫，所以任何服务里新加的守卫都能被覆盖，
	// 而不只是本包里的那些。
	// 测试跑在本包目录下，仓库根在上面三层；
	// 从那里扫才能覆盖任何服务新加的守卫，
	// 而不是只有本包的那些。
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

	// 只为证明"拒绝确实生效"而存在的固定权限码。测试要的是一个谁都没持有的权限，
	// 那说明测试成功了，而不是漏了种子数据。
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

// 没人持有的权限就是没人能用的路由；
// 没人能被授予的权限，就是永远没法通过角色编辑器修好的路由。
// 所以计量声明恰好只发给那些负责接入硬件的角色，
// 好让"能给站点定价的人"不会自动变成"能认定它那些板子能计量什么的人"——
// 否则能力校验就只是走个形式。
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
