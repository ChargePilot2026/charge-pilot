#!/usr/bin/env -S npx tsx
/**
 * check-api-consistency.ts · API ↔ DB ↔ Stream 三方一致性检查脚本
 *
 * 配套文档:docs/cross-reference.md(API ↔ DB ↔ Stream 一致性对账)
 *
 * 检查项:
 *   1. api/*.md 中出现的所有 _stream 名都在 cross-reference.md § 1 列表内
 *   2. db/*.md 中出现的所有表名都在 cross-reference.md § 2 列表内
 *   3. 写端点(POST/PUT/DELETE)都有对应表承接(cross-reference.md § 4)
 *   4. 跨服务调用路径在调用方文档中被引用为"由 X.md 定义"而非重复声明
 *
 * 用法:
 *   npx tsx tools/check-api-consistency.ts
 *
 * 退出码:
 *   0 = 一致
 *   1 = 不一致(打印问题列表)
 *
 * 实现状态:占位实现(逐项待补);MVP 阶段先打通骨架,逐步把规则落地。
 */

import * as fs from 'node:fs';
import * as path from 'node:path';

const ROOT = path.resolve(__dirname, '..');
const DOCS = path.join(ROOT, 'docs');
const CROSS_REF = path.join(DOCS, 'cross-reference.md');
const API_DIR = path.join(DOCS, 'api');
const DB_DIR = path.join(DOCS, 'db');

const KNOWN_STREAMS = new Set([
  'device_event_stream',
  'alert_stream',
  'charge_started_stream',
  'charge_ended_stream',
  'refund_required_stream',
  'invoice_required_stream',
  'webhook_retry_stream',
  'ota_schedule_stream',
  'comp_tx_stream',
]);

const KNOWN_TABLES_BY_SCHEMA: Record<string, Set<string>> = {
  user_db: new Set([
    'user', 'port_view', 'wallet_account', 'wallet_txn',
    'coupon', 'coupon_grant', 'membership_card', 'charge_order',
    'payment_order', 'refund_record', 'refund_reconcile_diff',
    'risk_freeze_log', 'invoice_request', 'callback_idempotent',
    'feedback', 'device_fault_report',
  ]),
  admin_db: new Set([
    'admin_user_role', 'role', 'permission', 'station', 'device_meta',
    'pricing_rule', 'pricing_template', 'coupon', 'split_template',
    'split_party', 'whitelabel_config', 'announcement',
    'customer_service_config', 'webhook_subscription',
    'webhook_delivery_log', 'ota_package', 'ota_schedule',
    'alert_rule', 'alert_subscription', 'risk_config',
    'settled_record', 'finance_reconcile_log', 'invoice_review',
    'alert_event', 'audit_log',
  ]),
  gateway_db: new Set([
    'vendor', 'device', 'device_session', 'telemetry',
    'telemetry_aggregate_15min', 'telemetry_aggregate_hourly',
    'raw_frame_log', 'ota_command',
  ]),
  billing_db: new Set([
    'fee_calculation', 'settlement', 'settlement_party_amount',
    'pricing_tier_snapshot', 'withdraw_request',
  ]),
  worker_db: new Set([
    'scheduled_task', 'task_execution_log', 'comp_tx_log',
    'dlq_log', 'retry_queue',
  ]),
};

interface Issue {
  level: 'error' | 'warning';
  file: string;
  message: string;
}

const issues: Issue[] = [];

function readFile(p: string): string {
  return fs.readFileSync(p, 'utf-8');
}

function extractStreamNames(text: string): string[] {
  // 匹配 `_xxx_stream` 形式(下划线 + 小写字母 + _stream)
  const re = /\b[a-z][a-z0-9_]*_stream\b/g;
  const matches = new Set<string>();
  let m: RegExpExecArray | null;
  while ((m = re.exec(text))) {
    matches.add(m[0]);
  }
  return Array.from(matches);
}

function extractTableNames(text: string, schema: string): string[] {
  // 匹配 `\`table_name\``(反引号包围)且在已知 schema 名后出现的表名
  const re = new RegExp(`\`(${schema}\\.[a-z_]+)\``, 'g');
  const matches = new Set<string>();
  let m: RegExpExecArray | null;
  while ((m = re.exec(text))) {
    matches.add(m[1].split('.')[1]);
  }
  return Array.from(matches);
}

function check1_knownStreams(): void {
  console.log('✓ 检查 1: api/*.md 中出现的 Stream 名都在 § 1 列表内...');
  const apiFiles = fs.readdirSync(API_DIR).filter(f => f.endsWith('.md'));
  for (const f of apiFiles) {
    const text = readFile(path.join(API_DIR, f));
    const streams = extractStreamNames(text);
    for (const s of streams) {
      if (!KNOWN_STREAMS.has(s)) {
        issues.push({
          level: 'error',
          file: `docs/api/${f}`,
          message: `出现未登记的 Stream 名 \`${s}\`(必须在 § 1 列表内 + 技术规格 § 5.1 登记)`,
        });
      }
    }
  }
}

function check2_knownTables(): void {
  console.log('✓ 检查 2: db/*.md 中出现的表名都在 § 2 列表内...');
  const dbFiles = fs.readdirSync(DB_DIR).filter(f => f.endsWith('.md'));
  for (const f of dbFiles) {
    const text = readFile(path.join(DB_DIR, f));
    for (const [schema, knownSet] of Object.entries(KNOWN_TABLES_BY_SCHEMA)) {
      const tables = extractTableNames(text, schema);
      for (const t of tables) {
        if (!knownSet.has(t)) {
          issues.push({
            level: 'error',
            file: `docs/db/${f}`,
            message: `出现未登记的表名 \`${schema}.${t}\`(必须在 § 2 列表内)`,
          });
        }
      }
    }
  }
}

function check3_writeEndpointsHaveTables(): void {
  console.log('✓ 检查 3: 写端点(POST/PUT/DELETE)都有对应表承接...');
  // 简化:扫所有 api/*.md 中形如 "### `POST /path`" / "### `PUT /path`" / "### `DELETE /path`" 的标题
  // 然后查找其后续内容中是否提到 "INSERT" / "UPDATE" / "写" + 表名
  // (本期占位实现:仅扫最常见的 admin 端点 + cross-reference.md § 4 校验)
  const apiFiles = fs.readdirSync(API_DIR).filter(f => f.endsWith('.md'));
  for (const f of apiFiles) {
    const text = readFile(path.join(API_DIR, f));
    const writeHeaderRe = /### `(POST|PUT|DELETE) (\/[^`]+)`/g;
    let m: RegExpExecArray | null;
    while ((m = writeHeaderRe.exec(text))) {
      const method = m[1];
      const path = m[2];
      // 取标题后 50 行,检查是否提到 INSERT/UPDATE + 表名
      const startIdx = m.index + m[0].length;
      const sectionEnd = text.indexOf('---', startIdx);
      const section = text.slice(startIdx, sectionEnd === -1 ? startIdx + 3000 : sectionEnd);
      const hasWriteOp = /INSERT|UPDATE|DELETE FROM|写(入|到|库|表)|插入|更新/.test(section);
      if (!hasWriteOp) {
        issues.push({
          level: 'warning',
          file: `docs/api/${f}`,
          message: `端点 ${method} ${path} 在其描述中未明确指出写入哪个表,可能导致 § 4 漂移`,
        });
      }
    }
  }
}

function check4_crossServicePaths(): void {
  console.log('✓ 检查 4: 跨服务调用路径不被重复声明(应由对方服务 API 文档定义)...');
  // 扫 api/*.md,检查是否有形如 "POST /api/v1/{user|admin|gateway|billing}/..." 出现在非本服务的文档中
  // 例:admin.md 写 "POST /api/v1/user/..." 应该是 OK 的(user 服务的端点被 admin 调用)
  // 但 admin.md 写 "POST /api/v1/admin/internal/..." 不行(自指 admin 内部路径应该由 admin.md 自身定义)
  // (本期占位实现:仅打印统计信息)
  console.log('  (本期占位:规则待后续补充)');
}

function main(): void {
  console.log('==== ChargePilot API 一致性检查 ====');
  console.log(`ROOT: ${ROOT}`);
  console.log();

  if (!fs.existsSync(CROSS_REF)) {
    console.error(`✗ 找不到 cross-reference.md: ${CROSS_REF}`);
    process.exit(1);
  }

  check1_knownStreams();
  check2_knownTables();
  check3_writeEndpointsHaveTables();
  check4_crossServicePaths();

  console.log();
  if (issues.length === 0) {
    console.log('==== ✓ 全部检查通过 ====');
    process.exit(0);
  }

  console.log(`==== ✗ 发现 ${issues.length} 个问题 ====`);
  for (const i of issues) {
    const tag = i.level === 'error' ? '✗' : '⚠';
    console.log(`${tag} [${i.level}] ${i.file}: ${i.message}`);
  }
  process.exit(1);
}

main();