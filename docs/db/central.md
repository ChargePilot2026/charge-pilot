# 当前数据库及精简清单

2026-10-01：原 user_db、admin_db、billing_db 合并为 central_db。领域模块继续独立，central 共用一个连接池；gateway_db、worker_db 保留服务边界。Redis 缓存与事件流保留独立实例。

| 数据库 | 业务表 | 用途 |
| --- | ---: | --- |
| gateway_db | 15 | 设备身份、指令、遥测、设备事件 |
| central_db | 85 | 用户钱包、充电订单、运营、计费结算 |
| worker_db | 5 | 任务租约、执行历史、消费审计与死信重放 |

实际旧开发库有 131 张业务表，清理 26 张后保留 105 张；每库另有 goose_db_version。旧空 chargepilot schema 同步移除。

## 手机号存储

`user.phone` 使用 `VARCHAR(11) NULL` 保存明文手机号，唯一索引 `uk_phone` 防止重复绑定。未绑定或解绑后为 NULL。已移除密文、哈希列及 `PHONE_ENCRYPTION_KEY` 配置；后台支持完整号码和片段搜索，用户绑定响应仍返回脱敏号码。

2026-10-01 开发库变更前已备份为 `.tmp/dev-db-before-plain-phone-20261001-181823.sql`，SHA-256 为 `FA017B39C2F2BCCF2896363DC90A9F110CD3B90F17F1912390D4E60069FDDC10`。原有 2 个用户均未绑定号码，无需解密回填；字段变更保留原有用户和业务记录，central 已重启。

## 删除清单

| 原库 | 删除的表 | 原因 |
| --- | --- | --- |
| admin_db | admin_coupon、customer、invoice_review、refund_task、settled_record、station_recharge_package | 无运行时访问且为空，已有当前业务模型覆盖相关职责 |
| user_db | audit_log、port_view、refund_execution、refund_reconcile_diff | 无运行时访问且为空；运营审计、设备端口与退款记录已有实际实现 |
| gateway_db | raw_frame_log | 无读写的空原始报文预留表 |
| worker_db | retry_queue | 重试使用实际任务、Outbox 和 Stream 机制，此表为空 |
| admin_db | charge_offer、pricing_package_template、station_policy、pricing_switch_task、pricing_switch_task_item | 旧价格/套餐/切换实现无注册入口，均为空；现行 charging scheme、pricing_rule、pricing_publication 保留 |
| admin_db | alert_rule、alert_subscription、customer_service_config、ota_package、ota_schedule | 旧开发库残留空表，当前初始化和路由已停用 |
| gateway_db | ota_command | 旧开发库残留空表，当前 OTA 未实现 |
| user_db | membership_card | 本期无会员业务且为空，删除资料接口与页面中的虚假会员状态 |
| admin_db | risk_config | 空且只有配置写入，实际钱包风控未读取；删除无效配置接口，保留现行风控冻结与审核 |
| billing_db | pricing_tier_snapshot | 14 条历史规则快照已逐条验证完整保存在 fee_receipt.source_json.rule 中，删除重复持久化 |

旧 pricing/package 后台路径转向当前充电方案页。移除旧实现、无效权限种子（含旧 pricing.template.create）及相应历史探针；当前退款、计费与定价通过 Go 集成测试验证。

## 保留及简化

用户事件表继续命名 event_outbox，运营事件表改为 admin_event_outbox，分别使用 user/admin publisher，防止同名表或相同 id 串写。钱包流水、订单/支付/退款幂等凭据、端口锁、计费来源与分账明细、审计日志、雪花 ID 持久状态均保留。

运营对用户资金、退款、优惠券、开票等操作的审计与业务变更在同一事务中写入；提现和 Webhook 重发也使用同一事务。设备网关操作跨服务，仍在网关响应后记录运营审计。

telemetry_aggregate_15min 与 telemetry_aggregate_hourly 使用生成列 port_key=COALESCE(port_no,0) 建唯一索引，设备级 NULL 端口仅有一个桶；API 仍保留 port_no=NULL。迁移按 SUM(avg_value*count)/SUM(count)、MIN、MAX、SUM(count) 合并重复桶，保留原样本数以及已无原始遥测的历史聚合数据。

删除没有 handler 的 alert_evaluate 计划；保留 webhook_dispatch。任务日志每小时分批清理：成功且无效果的记录保留一天，完成的其他记录保留三十天；运行中的任务不删除。

## 本地数据维护

本次五库合并已经完成，专用一次性迁移脚本已删除。过程和迁移前完整 SQL 备份记录保留在下节；这些旧备份对应五库结构，回退时应先在独立 MySQL 恢复核验，并使用原版本代码和配置。

日常三库备份、恢复使用 scripts/db/backup.sh、scripts/db/restore.sh。修改初始化结构后，可明确执行 scripts/db/reset-dev.ps1 -ResetDevelopmentData 重建开发库；该命令会备份后清空开发库及 Redis。需要保留数据的后续结构变更应另行编写迁移。

## 本次实施验证

本地开发库已实际迁移为 gateway_db / central_db / worker_db，共 105 张业务表。85 张 central 表的迁移前后行数逐表一致；权限与无效任务随后按清理清单移除。两个聚合表的重复桶均为 0，样本数、加权均值和极值在切表前完成校验。实际备份中的 14 条计费规则与收费凭据 JSON 严格相等。

迁移前完整备份为 `.tmp/dev-db-before-compact-20261001-165553.sql`（11,405,944 字节），SHA256 为 `40174A99B26DA1D895CC8EB977664FC721439E4BA2245CB787CC49B9F8C4762C`；同名 JSON 保存保留表行数。备份恢复和迁移流程先在独立容器演练通过。当次迁移的重复执行已验证为不改数据；一次性工具已在目录整理中删除。

隔离 MySQL / Redis 的全量 Go 回归、后续受影响包测试、gofmt 和 go vet 通过。管理端类型检查、构建及 63 项测试通过，小程序类型检查及 59 项测试通过。三个后端 readiness、后台登录及站点/设备/用户/订单/优惠券/角色/权限查询通过，当时的开发/生产/示例 Compose 配置校验通过；重复 examples 配置现已删除。测试与演练容器已清理。

## central_db 完整表清单

| 表 | 用途 |
| --- | --- |
| `active_port_charge` | 端口当前充电占用(跨月唯一性兜底) |
| `card_charge` | 在线卡充电会话与累计购买时长 |
| `card_operation` | 在线卡刷卡操作幂等结果和扣款记录 |
| `charge_bill` | 充电账单 |
| `charge_bill_read` | 账单已读标记 |
| `charge_billing_cutoff` | 订单首次计费截止点 |
| `charge_billing_job` | 待结算订单任务及重试状态 |
| `charge_debt` | 充电欠费 |
| `charge_debt_receipt` | 欠费补缴入账回执 |
| `charge_end_receipt` | 设备结束事件的幂等回执 |
| `charge_event_log` | 订单状态事件及时间线 |
| `charge_fee_receipt` | 计费消费事件幂等回执 |
| `charge_manual_settlement` | 无法自动计量时的人工最终结算与审计 |
| `charge_meter_review` | 充电计量异常核对 |
| `charge_order` | 充电订单(内部生命周期及独立业务、支付状态) |
| `charge_order_pricing` | 订单冻结的完整方案与计算快照 |
| `charge_payment_intent` | 支付前冻结方案及端口预占 |
| `charge_port_lock` | 支付与在线卡共享的端口事务锁 |
| `charge_prepay` | 预付支付确认结果 |
| `charge_start_receipt` | 设备启动确认和幂等摘要 |
| `coupon` | 优惠券模板 |
| `coupon_activity_rule` | 优惠券活动规则及版本 |
| `coupon_grant` | 优惠券发放记录 |
| `coupon_grant_request` | 优惠券发放请求幂等回执 |
| `coupon_redemption` | 优惠券核销记录 |
| `device_fault_report` | 设备报修 |
| `device_fault_report_event` | 设备报修状态与巡检处理记录 |
| `event_outbox` | 事件 outbox(可靠发布) |
| `feedback` | 评价/投诉 |
| `invoice_admin_review` | 发票审核记录 |
| `invoice_request` | 发票申请 |
| `manual_refund_request` | 人工退款申请及审核 |
| `online_card` | 用户在线卡绑定和挂失状态 |
| `online_card_audit` | 在线卡操作审计 |
| `payment_callback_idempotent` | 微信支付回调幂等(30 天保留) |
| `payment_order` | 支付订单(支持 charge / wallet_recharge) |
| `refund_record` | 退款记录 |
| `refund_rejection` | 退款拒绝原因记录 |
| `refund_review` | 退款审核过程 |
| `refund_success_receipt` | 退款成功的幂等确认 |
| `risk_freeze_log` | 风控冻结记录(本期仅频次触发) |
| `snowflake_state` | Snowflake 编号分配状态，业务事务内行锁串行分配 |
| `charge_debt_payment_request` | 欠费支付请求幂等回执 |
| `user` | 终端用户 |
| `user_login_identity` | 用户登录身份关联 |
| `wallet_account` | 钱包账户(1:1 with user) |
| `wallet_recharge_request` | 钱包充值请求幂等键 |
| `wallet_refund_part` | 钱包退款按原支付渠道拆分明细 |
| `wallet_refund_request` | 钱包退款请求及处理结果 |
| `wallet_risk_freeze_link` | 钱包风控冻结关联 |
| `wallet_risk_release` | 钱包风控解除记录 |
| `wallet_risk_review` | 钱包风控审核记录 |
| `wallet_txn` | 钱包流水 |
| `admin_data_scope` | 后台账号数据范围 |
| `admin_field_mask` | 角色字段脱敏规则 |
| `admin_user_role` | 管理员账号 |
| `alert_event` | 告警事件 |
| `announcement` | 公告 |
| `audit_log` | 审计日志 |
| `device_import` | 设备导入请求及执行重试状态 |
| `device_import_identity` | 设备导入时的请求身份和参数校验快照 |
| `device_meta` | 设备元数据(冗余自 gateway_db) |
| `admin_event_outbox` | 事件 outbox(可靠发布) |
| `export_task` | 导出任务 |
| `finance_reconcile_log` | 财务对账日志 |
| `permission` | 权限码 |
| `pricing_publication` | 计费规则发布版本和请求幂等摘要 |
| `pricing_rule` | 计费规则 |
| `pricing_template` | 计费模板 |
| `regulatory_report` | 监管报送持久队列 |
| `role` | 角色 |
| `role_permission` | 角色-权限映射 |
| `split_party` | 分账参与方 |
| `split_template` | 分账模板 |
| `station` | 充电站点 |
| `webhook_delivery_log` | Webhook 投递日志 |
| `webhook_subscription` | Webhook 订阅 |
| `whitelabel_config` | 白标配置 |
| `fee_calculation` | 计费明细 |
| `fee_delivery` | 计费结果投递任务 |
| `fee_receipt` | 计费事件幂等回执 |
| `manual_fee_review` | 人工定价兜底单(D16) |
| `settlement` | 分账汇总 |
| `settlement_party_amount` | 分账参与方金额 |
| `withdraw_request` | 提现申请 |
