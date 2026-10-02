// 刷卡所需的端口实时状态抽象。端口发现与扫描能力归属 scan 家族
// （charge.ScanAPI），card 包只依赖本接口，由 cmd/central 接线注入，
// 避免 card 反向依赖 charge。
package card

import (
	"context"
	"regexp"
)

// portCodePattern 校验刷卡请求中的端口编码字符集与长度。
// 与 charge 侧 scan 家族的 userScanCodePattern 同口径，属各自输入契约，独立维护。
var portCodePattern = regexp.MustCompile(`^[A-Za-z0-9_:-]{1,64}$`)

// PortRef 是刷卡事务所需的最小端口事实集合（来自 scan 家族的实时查询）。
type PortRef struct {
	Found        bool   // 端口是否解析成功（scan 侧返回 200 且端口存在）
	Kind         string // 解析结果类型，刷卡要求 "port"
	DeviceID     string
	StationID    uint64
	DeviceStatus string // 设备服务状态，如 enabled
	PortID       string
	PortNo       uint8
	Online       bool
	Available    bool
}

// PortLookup 由 scan 家族实现：按端口编码查询实时端口状态，第二返回值为 HTTP 状态码语义。
type PortLookup interface {
	LookupPort(ctx context.Context, portID string) (PortRef, int)
}
