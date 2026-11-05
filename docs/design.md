# CPA model-meta-sync 插件设计与验收

> 目标：在 CPA 侧「获取模型列表」时，把每个 OpenAI 兼容供应商自己的 `/v1/models` 返回参数同步进
> CPA 供应商配置（`api-keys.openai-compatibility[].models[]`），避免手工逐条填 `display-name` /
> `max-context-length` / `input-modalities` / `output-modalities` / `thinking.levels`。

- 插件 ID：`model-meta-sync`
- 版本：0.3.2（ABI 1 / schema 8，CPA v8）
- 产物：`dist/model-meta-sync_<version>_<goos>_<goarch>.zip`（商店标准包，内含 `<id>.<ext>`）
  与 `plugins/linux/amd64/model-meta-sync-v<version>.so`（仓库内便利产物，linux/amd64）
- 源码：`plugin/`
- 0.2.0 变更：取消供应商白名单（默认同步配置内全部启用的 provider）；供应商只按名称命中；
  模型名匹配改为 dsh-model-metadata 式回退（裸名 / 去分隔符 / 目录后缀 / 部署限定词，歧义即拒绝）；
  单个上游失败隔离，不再整批中止；报告与日志不再输出 `base_url`/主机/IP。
- 0.3.1 变更：按 CPA 插件商店标准打包与登记（zip 根目录单库 + `checksums.txt` + `registry-entry.json`）；
  注册 `Logo` 与完整 `ConfigFields`；`sync_on_start` 缺省值修正为 true；版本号由 tag 经 ldflags 注入。
- 0.3.2 变更（评审修复）：目录后缀折叠改为单向；模型以 `name`（上游 id）为准、`alias` 仅在无 name 时使用；
  `fields`/`overwrite_fields` 未知字段名报错；插件关闭/重新注册时停止并等待后台循环、取消进行中的同步；
  新增按供应商的 `api_keys`，未限定供应商的 `api_key` 拒绝加载；CI 增加 race 检测。

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
| `id` | `name` | 精确匹配 → 大小写不敏感 → 裸名 → 去分隔符 → 目录后缀 → 部署限定词；歧义键拒绝匹配。不做 owned_by 拼接 |
| `display_name`（回退 `display-name`、`name`） | `display-name` | 空则填；`overwrite_fields` 命中时覆盖。`name` 与 id 完全相同则忽略 |
| `context_length`（回退 `context_window`/`max_input_tokens`） | `max-context-length` | 0 则填；命中覆盖。**注意 `max_output_tokens` 是输出上限，不是上下文** |
| `modalities.input` | `input-modalities` | 上游缺失则跳过（不猜 `["text"]`） |
| `modalities.output` | `output-modalities` | 同上 |
| `reasoning=true` 且 `reasoning_levels` 非空 | `thinking.levels` | 仅补空 / 命中覆盖；`reasoning=false` 不用空数组覆盖（CPA 对 `thinking: nil` 默认注入 low/medium/high） |
| `max_output_tokens` / `native_endpoints` / `pricing` / `service_tiers` | — | 无落点，只在报告里体现为「不可同步」 |

合并策略：
- `mode: missing`（默认）：只补空字段，绝不覆盖已有值。`overwrite_fields` 缺省为空集
  （空列表不会被当成「全部字段」）。
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
| U1–U20 | Go 单测：字段解析、missing/always/overwrite_fields、语义 diff 拒绝与 provider 白名单、思考档位路径、分页、冲突检测、URL 拼接、provider key、锚点/条目级别名、`disabled: yes/on`、多文档 YAML、备份保留 | 20/20 PASS |
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

> 沙箱套件：`.sandbox/run_suite.sh`（27 项断言全 PASS）；单测：`go test ./...`（20 项）。
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
- 0.1.1（13:39）：并发串行化、YAML 锚点/别名保护、provider 级 diff 校验、重复上游 id 取首条；启动同步 0 变更、未写文件。
- 0.1.2（15:00）：按独立验收修复回滚脚本（版本 glob + 只选 pre-install 备份 + 恢复后断言）、provider 级别名预扫描、
  `disabled: yes/on`、多文档 YAML 拒绝、diff 深度 fail-closed、ConfigFields 补全、`/config` 打码、部署脚本幂等更新；
  启动同步 0 变更；独立验收复核见 `recon/independent-acceptance.md`。
- 0.1.3（15:12）：按独立验收 v2 修复回滚脚本退出码/顺序/结构化判据、回滚改为外科式摘块（保留面板新增 key）、
  锚点检测改为全局（含跨 provider 条目级共享锚点）；启动同步 0 变更。
- 0.1.4（15:19）：按独立验收 v3 修掉 `--restore` 在无 python3 时静默成功（兜底 status ≠ 0/1 → 拒绝），
  报告新增 `warnings[]`（锚点守卫命中时显式告警，避免「同步静默失效」）；启动同步 0 变更。
- 证据：`evidence/production-deploy.json`、`evidence/integration-suite-*.txt`；运维步骤见 `runbook/deploy.md`。

## 5.5 0.3.0 变更

- 模型列表发现与 CPA／CPAMC 的「从端点拉取」对齐：先试 `<base>/v1/models`，仅 404/405 时回退
  `<base>/models`（CPAMC 的 `buildModelsEndpoint` 规则），支持讯飞星火这类根路径为 `/v2` 的网关。
- 新增 `models_paths`（provider 名称 → 路径）覆盖特殊目录；报告新增 `models_path` 字段（只有路径，无主机）。
- 与「从端点拉取」的绑定方式：面板应用模型会写 `config.yaml` → CPA 热重载 → 插件重新注册并执行启动同步，
  新模型的 metadata 在同一次操作内补齐（沙箱实测：运行中的 CPA 新增模型后一个重载周期内被补全）。
- 非 404/405 的上游错误不做路径重试，避免对真实故障重复请求。

## 5.4 0.2.2 上线记录

- 现场：workbuddy2api 的 `/v1/models` 用 `supports_reasoning` + `reasoning_supported_efforts`
  + `only_reasoning` + 单个 `reasoning_effort` 表达推理能力；0.2.1 只认 `reasoning` /
  `reasoning_levels` / `supported_reasoning_levels[].effort`，导致这些模型没有 `thinking`，
  面板无 Thinking 标记。
- 0.2.2：识别 `supports_reasoning` / `only_reasoning`；档位增加 `reasoning_supported_efforts`
  回退；单档模型（`only_reasoning` + `reasoning_effort`）写成该单档；档位名按 CPA 规范过滤
  （none/minimal/low/medium/high/xhigh/max，小写）。
- 应用结果：workbuddy2api (cn) 4 条新增 thinking、`global:kimi-k3` 由 3 档校正为上游唯一档 medium；
  写后热重载触发的第二次同步 `changed_fields=0`。

## 5.3 0.2.1 上线记录

- `config_path` 改为可选：未配置时从宿主进程取 `-config/--config`，否则用工作目录下的
  `config.yaml`（与 CPA 自身的回退一致）；显式配置仍然优先，两者都没有则报错而不是猜路径。
- 生产已删除配置块里的 `config_path`，重启后 `plugin loaded version=0.2.1`，启动同步
  `changed_fields=0`、未写盘，8 家供应商照常命中。
- 沙箱（无 `config_path`、工作目录启动）27/27 PASS。

## 5.2 0.2.0 上线记录

- 现场：生产 CPA 8 个 openai-compatibility provider（含 2 个 workbuddy2api，同一地址不同 key）。
- 首轮 dry run：7 家拉到模型列表，1 家（讯飞星火，base-url 以 `/v2` 结尾）返回 404，按 provider 隔离；
  供应商报告只列名称与计数，无地址。
- 应用后写入 12 处空字段（workbuddy2api cn/go 的 display-name、input-modalities、
  max-context-length），Magpie 6 条已填值零改动；第二次启动同步 `changed_fields=0`、未写盘。
- 启动 dry-run 开关 `dry_run_on_start` 为首次全量铺开提供了「只报告不落盘」的一步。
- 沙箱集成套件 27/27 PASS（真实 CPA arm64 + mock 上游），amd64 构建在独立 amd64 主机完成并通过单测。

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
6. 0.2.0 起锚点/别名守卫按 provider 生效：命中者被跳过并在 `warnings[]` 写出其名称，其余照常同步。
7. 模型目录只探测 `<base-url>/v1/models` 与 `<base-url>/models`；其它路径需在 `models_paths`
   里显式登记，未登记的 provider 作为 upstream error 跳过。

## 6.1 独立验收

`recon/independent-acceptance.md`（736 行，v1→v4 四轮）：v3 判定「可上线」，v4 确认 0.1.4 覆盖 P1；
累计回归 25 PASS / 0 FAIL。四轮均由非作者子代理在冻结源码 + 独立构造 fixture 上完成。

## 7. 部署与回滚

见 `runbook/deploy.md`。回滚：停用 `plugins.configs.model-meta-sync` 并删除 `.so`，或直接恢复
`config.yaml.bak-<ts>` 后重启。

## 8. 插件标准合规（0.3.1）

CPA 插件标准由三部分组成，缺一项都会在商店/管理面表现为「不标准」：

| 面向 | 标准要求 | 本项目落地 |
|---|---|---|
| 注册元数据 | `Metadata.Name` / `Version` / `Author` / `GitHubRepository` 必填；`Logo` 为展示图标；`ConfigFields` 供管理端生成配置表单 | `plugin/main.go` 的 `metadata()`；`Logo` 指向 `assets/logo.png`；14 个字段含类型、默认值、枚举与说明；`plugin/metadata_test.go` 校验「已解析的配置键 == 已登记字段」且默认值与 `parseConfig` 一致 |
| 发布产物 | release tag 为 `v<version>`（点分数字）；资产 `<id>_<version>_<goos>_<goarch>.zip`，zip 根目录仅一个动态库；同名 release 附 `checksums.txt`（sha256sum 格式） | `make dist` / `make checksums` / `make verify-dist`（打包工具 `plugin/cmd/zipdist`，只用标准库，跨平台一致）；`.github/workflows/build.yml` 在 tag 上构建 linux/amd64、linux/arm64、darwin/amd64、darwin/arm64、windows/amd64 并发布 |
| 商店登记 | `registry.json` 条目：`id`/`name`/`description`/`author`/`repository` 必填，`logo`/`homepage`/`license`/`tags` 可选；`id` 需匹配插件 ID 规则 | `store/registry-entry.json` + `store/README.md`（含用 CPA 自带校验器本地复核的方法） |

校验证据：0.3.1 的 zip 与条目已用 CPA v8.0.47 源码中的 `internal/pluginstore`（`ParseRegistry`、
`InstallArchive`）实测通过——条目被接受，zip 被安装为
`plugins/linux/arm64/model-meta-sync-v0.3.1.so`；并在真实 CPA v8.0.47 沙箱中确认
`GET /v0/management/plugins` 返回 `logo` 与 14 条 `config_fields`，启动 dry-run 只报告不落盘，
`POST /v0/management/model-meta-sync/sync` 写入后触发 `config successfully reloaded`，二次同步
`changed_fields=0`。
