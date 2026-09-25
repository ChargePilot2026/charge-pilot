# docs 一致性 CI 守门脚本

> **入口**:`tools/check-api-consistency.ts`
> **配套**:`docs/cross-reference.md` § 6(文档维护规则)
> **目标**:docs 一致性自动化的最小起步;后续按需扩充(详见下方"路线图")。

---

## 跑法

```bash
# Node.js >= 22(支持 --experimental-strip-types)
node --experimental-strip-types tools/check-api-consistency.ts
```

退出码:
- `0` = 全部通过
- `1` = 有错误(GitHub Actions 自动失败 CI)

---

## 当前检查项(3 条)

1. **Stream 名总账**
   - 扫 `api/*.md` 和 `db/*.md` 引用的所有 `*_stream` 名字
   - 若不在 `cross-reference.md` § 1 → 报错
   - 头部数字声称 vs 实际清点不一致 → 报错

2. **表数对账**
   - `cross-reference.md` § 2 标题数字 vs § 2.x 各小节清点
   - `cross-reference.md` § 2.x 各小节声称 vs 对应 `db/*.md` 实际

3. **写端点声明 ↔ API 文档**
   - 核对 `cross-reference.md` § 4 表格声明的写端点是否在 `api/*.md` 的端点清单或标题中出现
   - 表格使用缩写路径时按方法与路径后缀匹配;此检查不验证请求/响应语义或数据库实际写入

裸 `§` 引用无法仅凭文本判断是同文件还是跨文件引用,不作为自动失败项;跨 schema 访问和接口语义仍需人工审查。

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
      - uses: actions/setup-node@v4
        with:
          node-version: 22
      - run: node --experimental-strip-types tools/check-api-consistency.ts
```

---

## 路线图(下个迭代补)

- [ ] OpenAPI ↔ `api/*.md` 闭环:启动服务暴露 `/api/docs/openapi.json` 后,本脚本对比路径
- [ ] 数据库 migration ↔ `db/*.md` 闭环:`sqlx migrate run` 后导出 schema → 与文档字段定义对比
- [ ] 文档链完整性:README.md 引用的所有路径必须存在
- [ ] 死链接:`docs/**/*.md` 中链接目标存在性
- [ ] 文档规模告警:单文件 > 3000 行建议拆分
