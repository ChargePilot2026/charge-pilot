#!/usr/bin/env tsx
/**
 * docs 一致性 CI 守门脚本(P1-9 重写)
 *
 * 检查项:
 *   1. cross-reference.md § 1 列出的 Stream 名 + 加粗项,在 api/*.md / db/*.md 中出现的 _stream 名必须 ∈ 此集合
 *   2. cross-reference.md § 2 标题数字必须 = § 2 各小节清点到的表数
 *   3. cross-reference.md § 4.x 写端点表中的端点必须在对应 api/*.md 中存在
 *   4. 裸 § 引用需要人工判断是否指向当前文件,不做易误报的自动拦截
 *   5. 跨 schema 访问由架构审查核对,本脚本不声称已验证
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
  const headerLine = text.slice(0, idx).split('\n').length - 1;
  const tableLines: string[] = [];
  for (let i = headerLine + 1; i < lines.length; i++) {
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

  const claimed = Number(cross.match(/^## § 1 Stream 名总账[^\n]*\*\*(\d+) 个\*\*/m)?.[1] ?? NaN);
  if (known.size !== claimed) {
    fail(CROSS_REF, 24, `§ 1 标题声称 ${claimed} 个 Stream,实际清点 ${known.size} 个`);
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
  const sec2 = cross.match(/^## § 2 数据库表总账\([^\n]*?\*\*(\d+)\s*张\*\*\)/m);
  if (!sec2) {
    fail(CROSS_REF, 27, '§ 2 标题数字未匹配;请确保格式 `## § 2 数据库表总账(...**N 张**)`');
    return;
  }
  const claimed = Number(sec2[1]);

  // § 2.x 各小节清点(支持加粗 **`表名`**)
  const subClaims = [...cross.matchAll(/^### 2\.(\d) ([a-z_]+)\((?:计划)?(?:\*\*)?(\d+)\s*张(?:\*\*)?\)/gm)];
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
  for (const m of sec4.split('\n').filter((line) => line.trim().startsWith('|')).join('\n').matchAll(endpointRegex)) {
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
      actual.add(ep);
    }
    for (const m of text.matchAll(/^\|\s*(POST|PUT|DELETE|PATCH)\s*\|\s*`(\/[^`]+)`\s*\|/gm)) {
      actual.add(`${m[1]} ${m[2]}`);
    }
  }

  // 检查 § 4 中每个声明的端点是否在 api/*.md 中真实存在
  for (const ep of declared) {
    const [method, shortPath] = ep.split(/\s+/, 2);
    const normalizedSuffix = shortPath
      .replace(/\{[^}]+\}/g, '__PARAM__')
      .replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
      .replace(/__PARAM__/g, '[^/]+');
    const matches = [...actual].some((candidate) => {
      const [actualMethod, actualPath] = candidate.split(/\s+/, 2);
      return actualMethod === method && new RegExp(`${normalizedSuffix}$`).test(actualPath);
    });
    if (!matches) {
      const crossIdx = cross.indexOf(ep);
      fail(CROSS_REF, crossIdx > 0 ? 1 : 0, `§ 4 声明端点 ${ep},但在 api/*.md 中不存在`);
    }
  }
}

// ---------- main ----------
function main() {
  console.log('[1/3] 检查 Stream 名总账...');
  checkStreams();

  console.log('[2/3] 检查表数对账...');
  checkTableCount();

  console.log('[3/3] 检查写端点 ↔ api/*.md 闭环...');
  checkWriteEndpoints();

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
