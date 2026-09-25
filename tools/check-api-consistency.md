# docs 一致性 CI 守门脚本

> **入口**:`tools/check-api-consistency.ts`
> **配套**:`docs/cross-reference.md` § 6(文档维护规则)
> **目标**:docs 一致性自动化的最小起步;后续按需扩充(详见下方"路线图")。

---

## 跑法

```bash
# 前置(标准):
#   Node.js >= 18,pnpm 已安装,工作区根目录有 package.json
# 安装一次依赖:
pnpm add -D -w tsx

# 跑一致性检查:
tsx tools/check-api-consistency.ts
```

退出码:
- `0` = 全部通过
- `1` = 有错误(GitHub Actions 自动失败 CI)

---

## 当前检查项(4 条)

1. **Stream 名总账**
   - 扫 `api/*.md` 引用的所有 `*_stream` 名字
   - 若不在 `cross-reference.md` § 1 / `技术规格.md` § 5.1 → 报错
   - 头部数字声称 vs 实际清点不一致 → 报错

2. **表数对账**
   - `cross-reference.md` § 2 标题数字 vs § 2.x 各小节清点
   - `cross-reference.md` § 2.x 各小节声称 vs 对应 `db/*.md` 实际

3. **写端点 ↔ 表对应**
   - 当前为轻量统计(跨文档端点完整闭环由 OpenAPI ↔ api/*.md 闭环接管,见下方"路线图")

4. **裸 § X.Y 引用**
   - 跨文档裸引用必须加文档名(见 `cross-reference.md` § 6.5)
   - `cross-reference.md` 内部引用静默放行

---

## GitHub Actions 接入

`/.github/workflows/docs-check.yml`(后续补):

```yaml
name: docs-check
on:
  pull_request:
    paths:
      - 'docs/**/*.md'
      - 'tools/check-api-consistency.ts'
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: pnpm/action-setup@v3
        with: { version: 9 }
      - uses: actions/setup-node@v4
        with:
          node-version: 20
          cache: pnpm
      - run: pnpm install --frozen-lockfile
      - run: tsx tools/check-api-consistency.ts
```

---

## 路线图(下个迭代补)

- [ ] OpenAPI ↔ `api/*.md` 闭环:启动服务暴露 `/api/docs/openapi.json` 后,本脚本对比路径
- [ ] 数据库 migration ↔ `db/*.md` 闭环:`sqlx migrate run` 后导出 schema → 与文档字段定义对比
- [ ] 文档链完整性:README.md 引用的所有路径必须存在
- [ ] 死链接:`docs/**/*.md` 中链接目标存在性
- [ ] 文档规模告警:单文件 > 3000 行建议拆分
