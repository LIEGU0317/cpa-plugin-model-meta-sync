# recon/raw — 原始证据与脱敏说明

本目录保存 2026-10-30 侦察/验收期间抓取的上游响应、冻结源码快照与对抗测试输出，用于复核
`recon/models-diff.md`、`recon/independent-acceptance.md` 中的每一条结论。仓库公开前已逐文件复核。

## 1. 从未进入仓库的材料

| 类别 | 说明 |
|---|---|
| 密钥 | 抓取时调用方 key 只存在于远端 shell 变量（`K=$(...)`），未写入任何文件；`cpa-magpie-provider.json` 的 `keys[].api-key` 在落盘时就已置 `***REDACTED***` |
| 管理密钥 | 所有 management API 调用使用 `$KEY` / `$K` 变量，记录的命令只有变量名 |
| 主机身份 | 真实主机名统一替换为 `<cpa-host>` / `<build-host>`；公网入口与内网地址替换为 RFC 5737 文档保留段（`198.51.100.0/24`、`192.0.2.0/24`） |
| 日志凭据片段 | `auth_index=<auth-index>`、`auth=<provider-id>:<auth-id>`、`auth=api_key=<redacted>` |

## 2. 已评估、保留的内容

| 内容 | 判断 |
|---|---|
| 上游通道 id（`owned_by`）、模型 id / display name | 厂商公开目录中的产品标识，不是个人信息；§5 的字段映射结论依赖它们 |
| `pricing` / `service_tiers` 数值 | 上游目录按厂商挂牌价给出；结论只依赖「CPA 无落点」，与数值无关 |
| `/srv/<service>` 部署路径 | 通用 Linux 服务路径，不含用户名或主机名 |
| 端口 `8317` / `3425` / `7509` | 服务默认端口与端口映射示例 |
| 作者标识 `LIEGU0317` | 仓库作者，LICENSE 与插件元数据中的署名，故意保留 |

## 3. 文件

- `magpie-models-raw.json` / `magpie-models-cursor-ua.json` / `magpie-models-claudedesktop-ua.json`
  —— 网关 `GET /v1/models` 在三种 User-Agent 下的响应原文（无 key、无 host）。
- `cpa-magpie-provider.json` —— CPA `api-keys.openai-compatibility[8]` 的 provider 块，`keys` 已脱敏。
- `frozen-manifest-prod-relevant.txt`、`src-frozen-*/` —— 每个版本上线时的冻结源码与 SHA256SUMS。
- `audit-*.txt`、`audit_*_test.go`、`audit-v*` —— 对抗/回归测试输出与用例。
- `artifact-hashes.txt` —— 构建产物哈希（主机名已占位）。

如需比对未脱敏原始件，请使用私有渠道中的副本；本目录即为公开索引。
