package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// requirePattern matches the permission argument of a route guard. The whole
// point of this test is to catch a permission that was named in code but never
// seeded into the permission table, and the only place such a string appears
// is the source.
var requirePattern = regexp.MustCompile(`Require\("([a-z][a-z0-9_.]*)"\)`)

// TestEveryGuardedPermissionExists is the guard for a failure mode that no other
// test can see: a route gated behind a permission code that was never inserted
// into the permission table is not a compile error and not a unit-test failure,
// it is an endpoint that answers 403 to everyone, permanently and silently.
//
// It happened here. A new route required "device.update", which did not exist —
// the system had device.read and device.import and no third device permission —
// so the endpoint could never be called by any role, including the superuser,
// because Require has no bypass. Nothing noticed because the endpoint had never
// been exercised.
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

	// Scanned from the repository root, so a guard added to any service is
	// covered and not just the ones in this package.
	// The test runs in this package's directory, so the repository root is three
	// levels up. Scanning from there covers a guard added to any service rather
	// than only the ones in this package.
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

	// Fixture codes that exist only to prove a denial works. A test asking for a
	// permission nobody holds is the test succeeding, not a missing seed.
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

// A permission nobody holds is a route nobody can use, and one nobody can be
// granted is a route that can never be fixed through the role editor. The
// metering declaration goes to exactly the roles that onboard hardware, so that
// whoever can price a station is not automatically the one who certifies what its
// boards can measure — otherwise the capability check is only advisory.
func TestMeteringPermissionGoesToTheRolesThatOnboardBoards(t *testing.T) {
	if os.Getenv("TEST_ADMIN_DATABASE_URL") == "" {
		t.Skip("disposable MySQL required")
	}
	adminDB := openFinanceDB(t, "TEST_ADMIN_DATABASE_URL")
	rolesWith := func(code string) map[string]bool {
		out := map[string]bool{}
		rows := []struct {
			Role string `gorm:"column:role_code"`
		}{}
		if err := adminDB.Table("role_permission rp").
			Select("r.code AS role_code").
			Joins("JOIN permission p ON p.id = rp.permission_id").
			Joins("JOIN role r ON r.id = rp.role_id AND r.deleted_at IS NULL").
			Where("p.code = ?", code).Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			out[row.Role] = true
		}
		return out
	}
	onboarding := rolesWith("device.import")
	if len(onboarding) == 0 {
		t.Fatal("no role holds device.import; the fixtures did not load")
	}
	metering := rolesWith("device.metering")
	if len(metering) == 0 {
		t.Fatal("device.metering exists but no role holds it, so the endpoint is unreachable")
	}
	for role := range onboarding {
		if !metering[role] {
			t.Fatalf("role %q onboards devices but cannot declare what they report, so a new board is unusable until an administrator edits the role", role)
		}
	}
	for role := range metering {
		if !onboarding[role] {
			t.Fatalf("role %q can certify metering but does not onboard devices, which is how the capability gate gets bypassed", role)
		}
	}
}
