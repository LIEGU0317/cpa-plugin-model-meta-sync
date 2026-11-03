# CPA model-meta-sync 插件设计与验收

> 目标：在 CPA 侧「获取模型列表」时，把 Magpie `/v1/models` 返回的完整参数同步进 CPA 供应商配置
> （`api-keys.openai-compatibility[].models[]`），避免手工逐条填 `display-name` / `max-context-length` /
> `input-modalities` / `output-modalities` / `thinking.levels`。

- 插件 ID：`model-meta-sync`
- 版本：0.1.1（ABI 1 / schema 8，CPA v8.0.47 commit `90c447d3`）
- 产物：`model-meta-sync-v0.1.1.so`（linux/amd64 生产 sha256 `d0c125d7…eb51bf`；linux/arm64 沙箱）
- 源码：本目录 `plugin/`

## 1. 结论先行

| 问题 | 结论 | 证据 |
|---|---|---|
| CPA 插件商店里有没有现成插件做这件事 | **没有** | `recon/plugin-landscape.md`：153 个插件全量分账，零命中；最接近的 `cpa-account-config-manager` 只写模型名、不写参数 |
| Magpie `/v1/models` 是否带完整参数 | **是**，34 条、21 个字段、不分页、需 Bearer 鉴权 | `recon/models-diff.md`；`recon/raw/magpie-models-raw.json` |
| 能落到 CPA 配置的字段 | 4 个：`display-name`、`max-context-length`、`input-modalities`、`output-modalities`、`thinking.levels`（共 5 项，其中模态分输入/输出两键） | `internal/config/config_types.go:1019` `OpenAICompatibilityModel` |
| 落不了的字段 | `max_output_tokens`（输出上限，CPA 无对应键）、`native_endpoints`、`pricing`、`service_tiers`、`supported_parameters` | 同上；`ModelInfo.OutputTokenLimit` 只由静态 registry / 专用抓取器赋值 |
| 写回方式 | 插件进程内直接改 `config.yaml`（原地写 + 校验 + 备份），依赖 CPA 的 fsnotify 热重载 | `internal/watcher/events.go:71`、`config_reload.go:79/148` |
| 为什么不走管理 API | 生产 `management.secret-key` 是 bcrypt 哈希，插件拿不到明文；host ABI 20 个方法里没有 config 读写 | `recon/plugin-landscape.md` B.3 |

## 2. 字段映射

| Magpie 字段 | CPA 键 | 规则 |
|---|---|---|
| `id` | `name` | 精确匹配（大小写不敏感兜底）；不做 owned_by 拼接（`GET /v1/models/{id}` 只认列表 id） |
| `display_name` | `display-name` | 空则填；`overwrite_fields` 命中时覆盖 |
| `context_length`（回退 `context_window`/`max_input_tokens`） | `max-context-length` | 0 则填；命中覆盖。**注意 `max_output_tokens` 是输出上限，不是上下文** |
| `modalities.input` | `input-modalities` | 上游缺失则跳过（不猜 `["text"]`） |
| `modalities.output` | `output-modalities` | 同上 |
| `reasoning=true` 且 `reasoning_levels` 非空 | `thinking.levels` | 仅补空 / 命中覆盖；`reasoning=false` 不用空数组覆盖（CPA 对 `thinking: nil` 默认注入 low/medium/high） |
| `max_output_tokens` / `native_endpoints` / `pricing` / `service_tiers` | — | 无落点，只在报告里体现为「不可同步」 |

合并策略：
- `mode: missing`（默认）：只补空字段，绝不覆盖已有值。
- `mode: always`：按上游重算 `fields` 内全部字段。
- `overwrite_fields: [..]`：在 `missing` 模式下对指定字段开覆盖（生产用它覆盖 display/context/模态，**不动 thinking**）。

## 3. 写回与安全

1. 读 `config.yaml` → `yaml.Node`（保留键序与注释），只改目标 model mapping。
2. 生成新文本后做**语义 diff 校验**：只允许 `...models.<i>.<field>` 与 `thinking.levels[.<j>]` 变化；
   `base-url`、`name`、`alias`、模型条数、其它 provider 任何变化 → 拒绝写。
3. 写前二次读文件比对 sha256；不一致（用户正在管理面板保存）→ 返回冲突，不覆盖。
4. 备份 `config.yaml.bak-modelmetasync-<ts>`（默认保留 10 份），保留原文件权限。
5. 先写 staging 临时文件（fsync），再**原地写**目标文件（`O_TRUNC`）：
   - 原子 rename 只会产生 inotify Rename/Remove，CPA 的 config watcher 只认 Write/Create/Rename，
     实测不会触发热重载，因此必须原地写；
   - 原地写失败时自动用 staging 副本回填。
6. CPA watcher 检测到 Write → `config successfully reloaded` → 客户端/模型注册表刷新。沙箱实测见 §5 T3。

## 4. 接口

均走 CPA 管理面鉴权（`X-Management-Key` 或 `Authorization: Bearer`）：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/v0/management/model-meta-sync/status` | 最近一次运行报告 + 生效配置（`api_key` 打码） |
| GET | `/v0/management/model-meta-sync/config` | 生效配置 + 原始 config_yaml |
| GET | `/v0/management/model-meta-sync/preview` | 干跑：算 patch 不落盘 |
| POST | `/v0/management/model-meta-sync/sync` | 拉上游 `/v1/models` 并写回；`?dry_run=1` 干跑，`?provider=X` 限定供应商 |

上游失败（非 2xx/超时）→ `ok:false`、HTTP 502、**不写文件**；配置里找不到指定 provider → 400。

## 5. 测试矩阵（沙箱：CPA v8.0.47 arm64 + mock Magpie + 真实 Magpie 响应快照）

| 编号 | 场景 | 结果 |
|---|---|---|
| U1–U12 | Go 单测：字段解析、missing/always/overwrite_fields、语义 diff 拒绝、思考档位路径、分页、冲突检测、URL 拼接、provider key 读取 | 12/12 PASS |
| T1 | 无管理密钥 401；带密钥 status 200 | PASS |
| T2 | preview 干跑：200、报告有变更、文件 sha256 不变 | PASS |
| T3 | sync 落盘：written=true、备份存在、语义值修正、日志 `config successfully reloaded` | PASS |
| T4 | 幂等：第二次 sync changed_fields=0、written=false | PASS |
| T5 | 上游 503：HTTP 502、ok=false、配置文件 sha256 不变 | PASS |
| T6 | 分页：mock 两页，上游 34 条全收、匹配 12 条 | PASS |
| T7 | 不存在的 provider：HTTP 400 | PASS |
| T8 | missing 模式：只填空字段，未 strip 的条目原样保留 | PASS |
| T9 | 同步后网关 `/v1/models` 仍 200 | PASS |
| R1 | 真实 Magpie（CPU 主机出网、插件 UA `cpa-model-meta-sync/0.1.0`）：34 条、字段与快照一致 | PASS |

> 沙箱套件：`.sandbox/run_suite.sh`（27 项断言全 PASS）；单测：`go test ./...`（12 项）。
> 证据：`evidence/integration-suite-arm64.txt`、`evidence/integration-suite-amd64.txt`（build-host `/tmp/mms-stage`，同版本二进制 + 同 mock）。

## 5.1 生产上线记录（2026-10-30 13:27 CST）

- 部署前配置 sha256 `f178b145…5228d8` → 部署后 `41d9d872…693fe9`；部署前备份
  `config.yaml.bak-modelmetasync-deploy-20261030-132722`，插件写前备份
  `config.yaml.bak-modelmetasync-20261030-132726`。
- 变更范围：新增 `plugins.configs.model-meta-sync` + 3 条 `max-context-length`（1000000→1048576：
  `auto/deepseek-v4.1-flash`、`auto/deepseek-v4.1-flash-1017`、`auto/deepseek-v4-pro-0813`）。
- 未变：9 个 provider、12 条 Magpie 模型、alias 集合、其它 provider 全部零改动；`/v1/models` 仍 31 条。
- 日志：`plugin loaded/registered plugin_id=model-meta-sync`；写后出现
  `server clients and configuration updated`；进程持续运行、配置哈希稳定（无写回环）。
- 0.1.1（13:39）复部署：加入并发串行化、YAML 锚点/别名保护、provider 级 diff 校验、重复上游 id 取首条；
  启动同步检测到 0 变更、**未写文件**（`config_changed: false`），证明生产幂等；旧 0.1.0 `.so` 由 host 自动清理。
- 证据：`evidence/production-deploy.json`、`evidence/integration-suite-*.txt`；运维步骤见 `runbook/deploy.md`。

## 6. 已知限制

1. **alias 全局撞名**：12 条 Magpie 别名里 8 条与其它 provider 重名（`deepseek-flash` 全局 5 条等），
   CPA 的 `/v1/models` 只展示 4 条 Magpie 条目。元数据同步不解决命名冲突；要暴露全部条目需给 provider
   设 `prefix` 或改唯一 alias（本次未改动，避免影响现有客户端）。
2. **thinking 覆盖有行为风险**：CPA 对不支持的档位做就近 clamp（`internal/thinking/validate.go:277`，
   平局取低档），把上游没有的 `medium` 删掉会让显式请求 medium 的客户端降到 low。因此生产默认对
   `thinking` 只补空，不覆盖；确认后可把 `thinking` 加进 `overwrite_fields`。
3. **输出上限无法同步**：CPA `OpenAICompatibilityModel` 没有输出 token 上限字段，Magpie
   `max_output_tokens` 无处落地（只影响 Codex 客户端的上下文展示）。
4. 插件安装/升级需要重启一次 CPA（插件 host 只在启动时扫描 `.so`）；数据同步本身不需要重启。
5. 上游 UA 会影响 Magpie 返回形状（claude-desktop UA 返回另一套目录）；插件固定用中性 UA。

## 7. 部署与回滚

见 `runbook/deploy.md`。回滚：停用 `plugins.configs.model-meta-sync` 并删除 `.so`，或直接恢复
`config.yaml.bak-<ts>` 后重启。
