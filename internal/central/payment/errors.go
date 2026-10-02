package payment

import "errors"

// ErrPaymentIntentConflict 表示支付意图与另一请求或活跃端口占用冲突。
// 定义于此供 order（订单编号分配）等家族共用，避免哨兵随逻辑文件迁移而漂移。
var ErrPaymentIntentConflict = errors.New("payment intent conflicts with another request or active port")
