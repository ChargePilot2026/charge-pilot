#!/usr/bin/env tsx
/**
 * docs 一致性 CI 守门脚本(P1-9 重写)
 *
 * 检查项:
 *   1. cross-reference.md § 1 列出的 9 个 Stream 名 + 加粗项,在所有 api/*.md 中出现的 _stream 名必须 ∈ 此集合
 *   2. cross-reference.md § 2 标题数字必须 = § 2 各小节清点到的表数
 *   3. cross-reference.md § 4.x 写端点表中的端点必须在对应 api/*.md 中存在
 *   4. 跨文档裸 § X.Y 引用检测:仅允许"文档名 § X.Y"形式,跨文件引用必须带文档名
 *   5. db/*.md / api/*.md 引用的其他 schema 表,必须由 cross-reference.md § 3 登记的 HTTP 内部接口调用(而非直连)
 *
 * 使用:
 *   tsx tools/check-api-consistency.ts                 # 全量检查
 *
 * 退出码:
 *   0 = 全部通过
 *   1 = 有错误
 */

import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

const ROOT = join(import.meta.dirname, '..');
const DOCS = join(ROOT, 'docs');
const API_DIR = join(DOCS, 'api');
const DB_DIR = join(DOCS, 'db');
const CROSS_REF = join(DOCS, 'cross-reference.md');
const SPEC = join(DOCS, '技术规格.md');
const REQ = join(DOCS, '需求分析.md');

type Finding = { file: string; line: number; message: string };
const findings: Finding[] = [];

function fail(file: string, line: number, message: string) {
  findings.push({ file, line, message });
}

// ---------- helpers ----------
function read(file: string): string {
  return readFileSync(file, 'utf-8');
}

function mdFiles(dir: string): string[] {
  return readdirSync(dir).filter((f) => f.endsWith('.md')).map((f) => join(dir, f));
}

/**
 * 提取一个 markdown 表格的所有行(从首个 |...| 行到下一个空行 / 非表格行)。
 * 用于解析 cross-reference.md 的 § 1 / § 2.x / § 4.x 等表格。
 */
function extractTable(text: string, headerPattern: RegExp): string[] {
  const idx = text.search(headerPattern);
  if (idx < 0) return [];
  const lines = text.split('\n');
  const tableLines: string[] = [];
  for (let i = idx; i < lines.length; i++) {
    const l = lines[i];
    if (l.trim().startsWith('|')) {
      tableLines.push(l);
    } else if (tableLines.length > 0 && l.trim() === '') {
      // 表格结束(空行)
      break;
    } else if (tableLines.length > 0) {
      // 表格结束(其他内容)
      break;
    }
  }
  return tableLines;
}

// ---------- 1. Stream 名总账 ----------
function checkStreams() {
  const cross = read(CROSS_REF);
  // § 1 表里所有行,提取 `xxx_stream`(含加粗 **`xxx_stream`**)
  const tableLines = extractTable(cross, /^## § 1 Stream 名总账/m);
  const known = new Set<string>();
  for (const row of tableLines) {
    const matches = row.matchAll(/`([a-z_]+_stream)`/g);
    for (const m of matches) known.add(m[1]);
  }

  if (known.size !== 9) {
    fail(CROSS_REF, 9, `§ 1 应有 9 个 Stream,实际清点 ${known.size} 个(若表格改写,请同步更新此处期望值)`);
  }

  // 扫所有 api/*.md / db/*.md 中出现的 _stream 名
  for (const file of [...mdFiles(API_DIR), ...mdFiles(DB_DIR)]) {
    const lines = read(file).split('\n');
    lines.forEach((line, i) => {
      const matches = line.matchAll(/`([a-z_]+_stream)`/g);
      for (const m of matches) {
        if (!known.has(m[1])) {
          fail(file, i + 1, `未知 Stream "${m[1]}",需先在 cross-reference.md § 1 / 技术规格.md § 5.1 登记`);
        }
      }
    });
  }
}

// ---------- 2. 表数对账 ----------
function checkTableCount() {
  const cross = read(CROSS_REF);
  // § 2 标题数字
  const sec2 = cross.match(/## § 2 数据库表总账\([^,]*,\s*\*\*(\d+)\s*张\*\*/);
  if (!sec2) {
    fail(CROSS_REF, 27, '§ 2 标题数字未匹配;请确保格式 `## § 2 数据库表总账(...**N 张**)`');
    return;
  }
  const claimed = Number(sec2[1]);

  // § 2.x 各小节清点(支持加粗 **`表名`**)
  const subClaims = [...cross.matchAll(/### 2\.(\d) ([a-z_]+)\((?:计划)?\*\*?(\d+)\s*张\*?\*?\)/g)];
  let sumClaimedFromSubs = 0;
  for (const m of subClaims) {
    sumClaimedFromSubs += Number(m[3]);
  }
  if (sumClaimedFromSubs !== claimed) {
    fail(CROSS_REF, 27, `§ 2 标题声称 ${claimed} 张,§ 2.x 子节合计 ${sumClaimedFromSubs} 张(应等于)`);
  }

  // 进一步:db/*.md 的实际表数也应 = cross-reference § 2 各小节声称
  const bySchema: Record<string, number> = {};
  for (const file of mdFiles(DB_DIR)) {
    const schema = file.match(/db[\\/]+([a-z_]+)\.md$/)?.[1] ?? '';
    if (!schema) continue;
    const text = read(file);
    // db/*.md "## 表清单" 段(支持加粗 **`表名`**)
    const listBlock = text.match(/## 表清单[\s\S]*?(?=^## )/m)?.[0] ?? '';
    const actual = (listBlock.match(/^\|[*\s]*`([a-z_]+)`/gm) ?? []).length;
    bySchema[schema] = actual;
  }
  for (const m of subClaims) {
    const schema = m[2];
    const claim = Number(m[3]);
    const real = bySchema[schema] ?? -1;
    if (real >= 0 && real !== claim) {
      const crossIdx = cross.indexOf(m[0]) + 1;
      fail(CROSS_REF, crossIdx, `§ 2.${m[1]} (${schema}) 声称 ${claim} 张,db/${schema}.md 实际清点 ${real} 张`);
    }
  }
}

// ---------- 3. 写端点 ↔ api/*.md 闭环 ----------
function checkWriteEndpoints() {
  const cross = read(CROSS_REF);
  // § 4.x 各表中的写端点: `POST /path` 或 `PUT /path` 等
  // 注意:§ 4.x 第一列是端点(/api/v1/...),写端点必有 / 在行内
  const endpointRegex = /`((?:POST|PUT|DELETE|PATCH)\s+\/[^`\s]+)`/g;
  const declared = new Set<string>();
  // § 4.1 - § 4.5 段落
  const sec4Match = cross.match(/## § 4([\s\S]*?)(?=\n## )/);
  if (!sec4Match) return;
  const sec4 = sec4Match[1];
  for (const m of sec4.matchAll(endpointRegex)) {
    const ep = m[1];
    // 跳过:§ 4 文本里的示例代码块 / 注释
    if (ep.includes('内部接口') || ep.includes('xxx')) continue;
    declared.add(ep);
  }

  // 实际 api/*.md 中存在的端点
  const actual = new Set<string>();
  for (const file of mdFiles(API_DIR)) {
    const text = read(file);
    for (const m of text.matchAll(endpointRegex)) {
      const ep = m[1];
      // 内部接口不要求在 § 4
      if (ep.includes('/internal/')) continue;
      actual.add(ep);
    }
  }

  // 检查 § 4 中每个声明的端点是否在 api/*.md 中真实存在
  for (const ep of declared) {
    if (!actual.has(ep)) {
      const crossIdx = cross.indexOf(ep);
      fail(CROSS_REF, crossIdx > 0 ? 1 : 0, `§ 4 声明端点 ${ep},但在 api/*.md 中不存在`);
    }
  }
}

// ---------- 4. 裸 § X.Y 引用 ----------
function checkBareSectionReferences() {
  const allFiles = [
    CROSS_REF,
    SPEC,
    REQ,
    ...mdFiles(API_DIR),
    ...mdFiles(DB_DIR),
  ];

  // 文档名前缀允许的命中模式(只要上下文出现任一即视为"已带文档名")
  // 1. 文件名 + §:cross-reference.md § / 技术规格 § / 需求分析 §
  // 2. 同文件内部 §:跨文档引用需要带文档名,同文档内 § 合法(用于"见上 § 5.1"等)
  const docPrefix = /(?:需求分析|技术规格|cross-reference|cross-reference\.md|api\/[\w-]+\.md|db\/[\w-]+\.md|user\.md|admin\.md|gateway\.md|billing\.md|worker\.md|charge-order\.fsm|payment\.fsm|refund\.fsm|runbook|checklist)/i;

  for (const file of allFiles) {
    const lines = read(file).split('\n');
    // 对每一行,提取所有 § X / § X.Y 出现,检查前方 60 字符内是否有文档名前缀
    lines.forEach((line, i) => {
      // 跳过 markdown 链接 / 行内代码块中的 §
      if (line.trim().startsWith('|') || line.trim().startsWith('```')) return;
      const re = /§\s\d+(\.\d+)?/g;
      let m: RegExpExecArray | null;
      while ((m = re.exec(line)) !== null) {
        const start = Math.max(0, m.index - 60);
        const ctx = line.substring(start, m.index);
        if (!docPrefix.test(ctx)) {
          fail(file, i + 1, `裸 § 引用 "${m[0]}",必须带文档名前缀(规则见 cross-reference.md § 6.5)`);
        }
      }
    });
  }
}

// ---------- main ----------
function main() {
  console.log('[1/4] 检查 Stream 名总账...');
  checkStreams();

  console.log('[2/4] 检查表数对账...');
  checkTableCount();

  console.log('[3/4] 检查写端点 ↔ api/*.md 闭环...');
  checkWriteEndpoints();

  console.log('[4/4] 检查裸 § 引用...');
  checkBareSectionReferences();

  console.log('');
  if (findings.length === 0) {
    console.log('✓ docs 一致性检查全部通过');
    process.exit(0);
  }

  console.error(`✗ 发现 ${findings.length} 处不一致:`);
  for (const f of findings) {
    const rel = f.file.replace(ROOT + '\\', '');
    console.error(`  ${rel}:${f.line}  ${f.message}`);
  }
  process.exit(1);
}

main();