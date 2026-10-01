package httpapi

// 统一响应错误码约定：1xxx 表示请求或凭证问题，2xxx 表示业务状态冲突，5xxx 表示服务端或依赖故障。
// 数值一经发布不得变更；新增错误码须先在全仓确认未被占用，并按语义归入对应区段。
const (
	// CodeUnauthorized 未认证或凭证无效，通常对应 HTTP 401。
	CodeUnauthorized = 1001
	// CodeVerificationFailed 验证码、MFA 或第二签等校验凭证未通过，通常对应 HTTP 400。
	CodeVerificationFailed = 1002
	// CodeForbidden 无权访问、账号冻结或超出数据范围，通常对应 HTTP 403。
	CodeForbidden = 1003
	// CodeNotFound 资源不存在，通常对应 HTTP 404。
	CodeNotFound = 1004
	// CodeBadRequest 参数缺失或非法，通常对应 HTTP 400。
	CodeBadRequest = 1005
	// CodeMethodNotAllowed 请求方法不被该接口支持，对应 HTTP 405。
	CodeMethodNotAllowed = 1006
	// CodeImportConflict 批量导入内容与既有配置冲突（如通信协议或站点计费方式不兼容），对应 HTTP 409。
	CodeImportConflict = 1009
	// CodeStateConflict 订单、指令或回执与当前状态冲突，对应 HTTP 409。
	CodeStateConflict = 2000
	// CodeResourceUnavailable 业务资源当前不可用（设备暂停服务、端口占用、支付意图失效等），对应 HTTP 409。
	CodeResourceUnavailable = 2001
	// CodePricingUnavailable 计费方案不可用或已下架，对应 HTTP 409。
	CodePricingUnavailable = 2004
	// CodeAccountLocked 账号处于保护锁定状态，对应 HTTP 423。
	CodeAccountLocked = 2006
	// CodeConflict 业务规则冲突（重复申请、状态已变化、身份或余额冲突等），对应 HTTP 409。
	CodeConflict = 2009
	// CodeTaskConflict 定时任务当前状态不允许触发（已暂停或正在执行），对应 HTTP 409。
	CodeTaskConflict = 2010
	// CodeDependencyFailed 会话、用户、支付、卡或网关调用等业务依赖失败，通常对应 HTTP 503。
	CodeDependencyFailed = 5001
	// CodeServiceUnavailable 存储、队列或上游接口不可用，通常对应 HTTP 503。
	CodeServiceUnavailable = 5003
)
