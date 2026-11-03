# CPA 插件生态排查 + v8 插件 ABI 能力矩阵

> 目标：确认「是否已有插件实现：获取模型列表时把上游 `/models` 的完整参数同步进 CPA 供应商配置」；并摸清 CPA v8.0.47 插件 ABI，判断自研可行挂点。
>
> 侦察基座
> - 源码：`/tmp/cpa-src`，`git rev-parse HEAD` = `90c447d3a8727d115aa7552ec93ce023736ed492`，`git describe --tags` = **`v8.0.47`**（线上版本/commit 与源码一致，已核对）。
> - 商店注册表：`/tmp/cpa-research/registry.json`，`schema_version: 1`，**153** 个插件。
> - 线上 CPA：`ssh cpa-host` 只读查看 `/srv/cliproxyapi/`；二进制 8.0.47（2026-10-29 15:36），插件目录 `/srv/cliproxyapi/plugins/linux/amd64/` 实存 `antigravity-priority-v1.2.18.so`、`keeper-v0.1.0.so`，与「只启用两个插件」一致。
>
> 全部结论标注 `path:line`（相对 `/tmp/cpa-src`）。凡未直接读源码验证的，标「推断」。

---

## 0. 结论先行（TL;DR）

| 问题 | 结论 |
| --- | --- |
| registry 里有没有插件已经做了「从上游 `/models` 拉参数并写回 CPA 供应商 models 条目」？ | **没有。** 153 个插件中零命中。 |
| 有没有插件**会写** `api-keys.openai-compatibility[].models[]`？ | **有，但是定向的**：`cpa-account-config-manager` 在「一键绑定」时把自己账号的模型目录写进它新建的渠道行。这是「插件能写供应商配置」的**存在性证明**，不是通用同步器。 |
| 插件能不能读到 CPA 发给上游的 `/models`？ | **不能直接读；而且对 openai-compatibility 通道，CPA 根本不发这个请求** —— 它的模型完全来自 `config.yaml`。 |
| 插件能不能改并持久化供应商配置？ | **不能经 host API**（host 只暴露 20 个方法，无 config 读写）。**能经管理 HTTP API**：`PUT/PATCH /v0/management/openai-compatibility`，需要管理密钥。 |
| 插件能否自己发 HTTP 到上游？ | **能**，两种方式：`host.http.do`（无目标域名限制）或插件内部直接用 `net/http`。 |
| v7 SDK 编的 `.so` 在 v8 能否加载？ | **能加载**（ABI 都是 1，schema 1 ≤ 8），但退化为 v1 语义（响应字符串转义、每 chunk 带 body）。新插件应直接对 v8 SDK 编译。 |
| 推荐路径 | **路径 3（外部同步器，走管理 API）** 为主，**路径 2（插件出 patch）** 作为可选前端，**路径 1（纯插件闭环）** 不推荐作为首发。 |

---

# A. 现有插件排查

## A.1 判定口径

「相关」只认两件事之一：
1. 插件**读或改**客户端可见的模型列表/目录（catalog 面）；
2. 插件**读写** CPA 供应商配置里的 models 条目（config 面）。

**不认**：路由/别名改写、按 key 的访问策略、quota/scheduler、usage 统计 —— 这些即使挂 `Model` / `Management` tag，也不碰模型元数据。

## A.2 153 个插件的全量分账

按 `id + tags + description` 全量过一遍，落账如下（**6 + 11 + 5 + 33 + 98 = 153**）：

| 分组 | 数量 | 是否与本任务相关 |
| --- | --- | --- |
| T1 模型目录/列表/元数据直改 | 6 | 部分相关（都是 catalog 面） |
| T2 路由/别名/按 key 访问策略 | 11 | 不相关（不动元数据） |
| T3 管理面/配置写入类 | 5 | **关键**（决定「插件能否写配置」） |
| T4 Provider 类（含 dynamic model discovery） | 33 | 不相关（只服务自己那一家的模型发现） |
| T5 其余（usage/quota/scheduler/interceptor/normalizer/auth/OAuth/compat） | 98 | 不相关 |

**T5 的 98 个不计入判定**，理由：其 tags 全部落在 `Usage / Quota / Scheduler / Interceptor / Normalizer / Auth / OAuth / Compatibility / Dashboard / Monitoring / Router(非 Model Router) / Executor(单模型语义)`，description 中无任何「模型列表」「模型目录」「供应商配置」动作；其中 `router` 类（如 `quota-router`、`key-router`、`smart-load-balancer`、`sticky-fill-first`）选的是**凭据**，不是模型元数据。
（若需逐条复核，可用 `/tmp/cpa-research/registry.json` + 下文 T1~T4 名单做差集。）

T1~T4 名单：

- **T1（6）**：`model-sort`、`models-cache-override`、`model-list-tweaker`、`daybreak-blue`、`cpa-model-dock`、`cpa-models-speed-test`
- **T2（11）**：`priority-auto-router`、`model-fallback-router`、`model-mapper`、`model-router`、`anti-model-fallback`、`key-model-access`、`cpa-key-policy`、`key-router`、`key-provider-access`、`key-account-bind`、`credential-tier-router`
- **T3（5）**：`cpa-account-config-manager`、`api-key-manager`、`cpa-secret-manager`、`embeddings-forward`、`cpa-subscription-tools`
- **T4（33）**：`gemini-cli`、`cliproxyapi-copilot`、`cursor`、`opencode-go-cliproxyapi`、`commandcode`、`kiro`、`kimi-tools`、`commandcode-go-cliproxyapi`、`cliproxy-antigravity`、`mirasim`、`clinepassbridge`、`mirasim-proxy`、`cpa-groq`、`zcode`、`workbuddy`、`openai-oauth`、`github-copilot`、`vertex-adc`、`kiro-ha`、`our-free-model`、`cpa-freetier-plugin`、`freetier2api`、`gflow`、`deepseek-cpa`、`commandcode-cliproxyapi`、`opencode-go-provider`、`oc2cpa`、`spx-cursor`、`kiro4cpa`、`cpa-auth-zai`、`opencode-go-oauth`、`cursor-agent`、`today`

## A.3 T1 逐条判定（6 个）

| 插件 | 判定 | 依据 |
| --- | --- | --- |
| `model-sort` | **部分相关** | README 自陈只排 `GET /v1/models` 等 catalog 响应；`hidden` 明确「只改列表，模型仍完全可请求」。见 A.4.1 |
| `models-cache-override` | **部分相关** | 只覆写「Codex 客户端拿到的 `models_cache`」里**已存在**字段，覆写数据来自 **GitHub 仓库的 JSON**，不是上游 `/models`。见 A.4.2 |
| `model-list-tweaker` | **部分相关** | 只做 catalog 的 reorder / `display_name` / hide；**但它调管理 API**（有管理密钥配置项），是路径 1 的先例。见 A.4.3 |
| `daybreak-blue` | **不相关** | 只在 Codex catalog 里「新增两个写死的模型」（Daybreak Blue / Red），纯静态 `ModelRegistrar` 类贡献，无读取上游参数动作。 |
| `cpa-model-dock` | **部分相关** | 做别名 + 白名单 + 自动升级；策略存在**插件自有 state 目录**，明确写「All model policy belongs in the plugin's state/API, not additional YAML alias or whitelist keys」。改 catalog 与请求，不改 CPA 供应商配置。见 A.4.4 |
| `cpa-models-speed-test` | **部分相关（仅读，且有参考价值）** | README（`/tmp/plugdocs/cpa-models-speed-test.md:10`）“Auto Channel & Model Discovery: Uses host callbacks (`host.auth.models` and `host.auth.list`) to list all upstream accounts and models”。即它**读**全量渠道/模型清单做 TTFT 测速，不写回任何元数据。其 `host.*` 用法是本项目「读现有模型」的现成范例。 |

## A.4 最接近的 4 个：源码/README 核实

### A.4.1 `model-sort` — 纯 catalog 排序/隐藏

README（`/tmp/plugdocs/model-sort.md`）：

- `:9` “the model catalog in a stable order instead of a different random order on every…”
- `:14` “CPA builds its catalog by ranging over a Go map, so `/v1/models` comes back in a…”
- `:114` “`hidden` only edits the listing: hidden models stay fully requestable, which is the catalog-only filtering asked for in”
- `:41-44` 覆盖 `GET /v1/models`（OpenAI / Anthropic / Grok Shell / Codex client 四个形态）

**判定：改的是「客户端看到的 catalog」，不写 config.yaml，不读上游 `/models`。**

### A.4.2 `models-cache-override` — Codex catalog 字段覆写

README（`/tmp/plugdocs/models-cache-override.md`）：

- `:3` “它在 Codex 客户端拿到 `models_cache` 之前，把你的修改叠到 CLIProxyAPI 当前要下发的模型目录上。”
- `:5` “**只能覆写上游目录里已经存在的模型，不能追加上游没有的模型。**”
- `:9` 覆写内容按模型拆分放在 `overrides/<模型 slug>.json`，示例 URL 指向 **GitHub raw**：`https://raw.githubusercontent.com/moxi000/models-cache-override/main/overrides/deepseek-v4.1-flash.json`
- `:13` 管理页「拉取此模型的 **GitHub** 配置」

**判定：改 catalog（且只改已有字段）；数据源是插件作者维护的 GitHub 仓库，不是上游 `/models`；不写 CPA 供应商配置。**

### A.4.3 `model-list-tweaker` — catalog 改写 + 管理 API 调用先例

README（`/tmp/plugdocs/model-list-tweaker.md`）：

- `:3` “reorder, rename (`display_name`), and hide models in the **global model catalog** across Claude Code … OpenAI (`/v1/models`) … Gemini … Grok”
- `:30-36` 配置项 `models[].display_name` / `hidden`（存在 `plugins.configs.model-list-tweaker`）
- `:40-41` “Values come from the management API (`POST /v0/management/quota/fetch`), so **a management key is required**.”
- `:47-49` 配置示例里出现 `quota.management_url: "http://127.0.0.1:8317"` 与 `quota.management_key`

**判定：改 catalog，不写供应商配置。** 但其「插件持有管理密钥并回调 CPA 管理 API」的做法，是**路径 1 的直接先例**（见 B.3）。

### A.4.4 `cpa-model-dock` — 别名/白名单，策略在自有 state

README（`/tmp/plugdocs/cpa-model-dock.md`）：

- `:5` “stable model aliases, model allow lists, and conservative automatic upgrades within a model family … on real CPA v8.0.44”
- `:53` “**All model policy belongs in the plugin's state/API, not additional YAML alias or whitelist keys.**”
- `:110` “Lists contain only allowed, routable models and published aliases cloned from routable targets. **Filtering a list is not an independent authorization boundary.**”
- `:112` “Stock v8.0.44's runtime DTO reports none, so its trusted-prefix set is empty” —— 它读的是**宿主运行时下发的模型/凭据广告**，不是上游 `/models`

**判定：改 catalog + 请求准入；信息源是 CPA 运行时 DTO；不写 config.yaml。**

## A.5 T3：插件到底能不能写 CPA 配置（关键发现）

### A.5.1 `cpa-account-config-manager` —— 唯一「写供应商 models 条目」的实存证据

README（`/tmp/plugdocs/cpa-account-config-manager.md`）：

- `:122` “一键绑定会 **upsert 一个 `openai-compatibility` CPA 渠道**，把模型发布到 CPA 路由：Base URL 为 `{base}/v1` … 同时**把已验证的模型目录写入渠道的模型列表**；重复绑定同一账号只会更新已有渠道、保留既有别名”
- `:150` “「发布到 CPA 路由」会 upsert 一个 `openai-compatibility` CPA 渠道 … 并把白名单模型写入渠道模型列表。”
- `:153` “**需要 Management Key 才能写渠道行**（插件会记住最近一次管理请求带来的密钥，仅存内存、不落盘、不记日志）”
- `:246` “基础功能依赖 CLIProxyAPI native plugin **ABI/schema v1**，以及 Auth list/get/save、Usage Plugin 回调和当前鉴权 Management API。”

**机制拆解（与源码对齐）**：插件注册自己的管理路由 → 路由被宿主的 `mgmt.Middleware()` 挡在管理密钥之后 → 插件在 `ManagementRequest.Headers` 里**拿到调用方带来的管理密钥** → 用该密钥回打宿主的 `PUT/PATCH /v0/management/openai-compatibility`。
对应源码：`sdk/pluginapi/types.go:1569-1580`（`ManagementRequest.Headers`）、`internal/api/server_management.go:155-158`（openai-compatibility 四个写路由）、`internal/api/server_management.go:28-29`（组级中间件）。

**判定：能写，而且已经有人写了。但它的写入是「为它自己绑定的账号创建/更新渠道行」，不是「跟随上游 `/models` 定时同步既有渠道的参数」。** 这正是我们与它的差距所在。

### A.5.2 `api-key-manager` —— 也是走管理 API

README（`/tmp/plugdocs/api-key-manager.md`）：

- `:22` “`GET/PUT /v0/management/api-keys`：读取和全量保存 API Key 列表”
- `:23` “`GET/PATCH /v0/management/plugins/api-key-manager/config`：读取和保存插件配置”

**判定：插件把管理 API 当作「配置读写总线」使用，是成熟模式，不是 hack。**

### A.5.3 `cpa-secret-manager` / `embeddings-forward` / `cpa-subscription-tools`

按 description 判定**不相关**：分别是 API Key/Token 面板、embeddings 转发、订阅额度导入。均不触碰模型元数据。（`cpa-secret-manager` 的 Model 面未在描述中体现，未做源码核实；若 lead 要求穷尽，可补核。）

## A.6 T4：Provider 类的「dynamic model discovery」到底做了什么

以三个最典型的为例，核对结果一致：**它们发现的是「自己这一家 provider 的模型」，结果为运行时内存，不落 config.yaml。**

| 插件 | 自述 | 落点 |
| --- | --- | --- |
| `cliproxyapi-copilot` | README `:38` “`ModelProvider`: **authenticated discovery from the Copilot `/models` endpoint**” | 通过 `ModelProvider.ModelsForAuth` 把模型喂给 CPA 运行时注册表；`cliproxyapi-copilot.md:233` 只是让用户去 `http://127.0.0.1:8317/v1/models` 验证 |
| `oc2cpa` | README `:87` `dynamic_models: false`；`:114` “`dynamic_models: true` to pull them automatically” | 同上：自己 provider 的模型清单 |
| `github-copilot` | description “connect CLIProxyAPI to GitHub Copilot” | 同上 |

源码侧对应能力：`sdk/pluginapi/types.go:431-434`（`ModelProvider.StaticModels` / `ModelsForAuth`）、`:445-462`（`AuthModelRequest` 带 `HTTPClient`，可去上游 `/models`）。
**注意**：这条路径的产出是「该 provider 自己的模型表」，且 `sdk/pluginapi/types.go:408-410` 明确 —— `ModelRegistrar` 的模型「Without an Executor, these models are published only through matching credentials after applying their model exclusions」。也就是说 **`ModelRegistrar` 不能用来给一个已存在的 openai-compatibility 渠道补元数据**。

## A.7 ★ 明确回答

> **registry 里有没有插件已经做了「从上游 `/models` 拉参数并写回 CPA 供应商 models 条目」这件事？**

**无。**

最接近的 3 个及差距：

| # | 插件 | 与目标的距离 |
| --- | --- | --- |
| 1 | **`cpa-account-config-manager`** | 唯一真的写到 `openai-compatibility[].models[]` 的插件，写回路径（管理 API + 管理密钥）可直接复用。**差距**：①触发是人点按钮，不是上游 `/models` 驱动；②只 upsert 它自己新建的渠道行，不改用户既有渠道；③只写模型名/白名单，不写 `max-context-length` / modalities / `thinking.levels` 等参数；④不放 patch/diff，是整行覆盖式 upsert。 |
| 2 | **`models-cache-override`** | 是「按模型覆写参数」的思路最接近的（字段级 merge、未改字段跟随上游、可 restore）。**差距**：①作用面是 Codex 客户端 `models_cache`，不是供应商配置；②数据源是 GitHub 上的 JSON，不是上游 `/models`；③只能改「目录里已存在」的模型，不能追加。 |
| 3 | **`model-list-tweaker`** | 覆盖了 `display_name` 这类元数据的改写，且示范了「插件持管理密钥回调 CPA 管理 API」。**差距**：①只动 catalog 的 3 个维度（顺序/显示名/隐藏）；②不触供应商配置；③无上游拉取、无 diff、无校验。 |

补充：`model-sort`、`cpa-model-dock` 属于同族但更远（纯 catalog 排序/过滤、别名策略在自有 state）。

---

# B. CPA v8 插件 ABI 能力矩阵

## B.1 Capability 全表（以 `sdk/pluginapi/types.go` 为准）

`Capabilities` 结构体共 **26 个字段**（`sdk/pluginapi/types.go:74-162`）。真实接口签名如下：

| # | Capability 字段 | 接口 / 类型 | 定义行 | 方法签名 | 调用时机 |
| --- | --- | --- | --- | --- | --- |
| 1 | `ModelRegistrar` | `ModelRegistrar` | `:411-413` | `RegisterModels(ctx, ModelRegistrationRequest) (ModelRegistrationResponse, error)` | 启动/reload 时注册静态模型；**无 Executor 时仅随凭据发布** |
| 2 | `ModelProvider` | `ModelProvider` | `:431-434` | `StaticModels(ctx, StaticModelRequest)` / `ModelsForAuth(ctx, AuthModelRequest)` | 注册静态模型；或按凭据发现模型（`AuthModelRequest.HTTPClient` 可打上游） |
| 3 | `AuthProvider` | `AuthProvider` | `:308-314` | `Identifier()` / `ParseAuth` / `StartLogin` / `PollLogin` / `RefreshAuth` | 凭据文件解析、登录、刷新 |
| 4 | `FrontendAuthProvider` | `FrontendAuthProvider` | `:475-478` | `Identifier()` / `Authenticate(ctx, FrontendAuthRequest)` | 入站请求鉴权（可用 `FrontendAuthProviderExclusive` 独占） |
| 5 | `FrontendAuthProviderExclusive` | `bool` | `:84` | — | 标记位 |
| 6 | `FrontendAuthProviderOmitBody` / `FrontendAuthOmitBody` | `bool` | `:89`,`:91` | — | schema ≥7 生效，省掉 body 传输 |
| 7 | `Scheduler` | `Scheduler` | `:521-523` | `Pick(ctx, SchedulerPickRequest)` | 内置调度器之前的选凭据 |
| 8 | `SchedulerAcrossPriorities` | `bool` | `:97` | — | 是否跨优先级取候选 |
| 9 | `ModelRouter` | `ModelRouter` | `:527-529` | `RouteModel(ctx, ModelRouteRequest)` | 模型→provider 解析之前改路由目标 |
| 10 | `Executor` | `ProviderExecutor` | `:701-707` | `Identifier()` / `Execute` / `ExecuteStream` / `CountTokens` / `HttpRequest` | 实际发请求 |
| 11 | `ExecutorModelScope` | `ExecutorModelScope` | `:165-174` | — | `both`/`static`/`oauth` |
| 12 | `ExecutorInputFormats` / `ExecutorOutputFormats` | `[]string` | `:107`,`:109` | — | executor 声明协议 |
| 13 | `RequestTranslator` | `RequestTranslator` | `:1124` | `TranslateRequest(...)` | 请求协议翻译 |
| 14 | `RequestNormalizer` | `RequestNormalizer` | `:1129` | `NormalizeRequest(...)` | 反向归一化 |
| 15 | `ResponseTranslator` | `ResponseTranslator` | `:1134` | `TranslateResponse(...)` | 响应协议翻译 |
| 16 | `ResponseBeforeTranslator` / `ResponseAfterTranslator` | `ResponseNormalizer` | `:1139` | — | 翻译前/后的响应归一化 |
| 17 | `RequestInterceptor` | `RequestInterceptor` | `:1144-1147` | `InterceptRequestBeforeAuth(ctx, RequestInterceptRequest) (RequestInterceptResponse, error)` / `InterceptRequestAfterAuth(...)` | 发上游前（选凭据前/后各一次） |
| 18 | `RequestInterceptorOmitBodyBefore/After` | `bool` | `:125`,`:129` | — | schema ≥7 |
| 19 | `RequestBodyPatcherBefore/After` | `RequestBodyPatcher` | `:1260` | 返回字节区间编辑 | schema v8 新能力 |
| 20 | `RequestInterceptorPatchBodyBefore/After` | `bool` | `:139`,`:143` | — | schema ≥8 才生效 |
| 21 | `RequestLifecyclePlugin` | `RequestLifecyclePlugin` | `:1150-1153` | `HandleRequestComplete(ctx, RequestCompletion) error` | 请求终结后异步一次 |
| 22 | `ResponseInterceptor` | `ResponseInterceptor` | `:1155-1157` | `InterceptResponse(ctx, ResponseInterceptRequest) (ResponseInterceptResponse, error)` | **成功的非流式响应在下发前**（含 `/v1/models`，见 B.2） |
| 23 | `StreamChunkInterceptor` | `StreamChunkInterceptor` | `:1160-1162` | `InterceptStreamChunk(ctx, StreamChunkInterceptRequest)` | 流式分片下发前 |
| 24 | `WebSocketResponseObserver` | `WebSocketResponseObserver` | `:1165-1167` | `ObserveWebSocketResponseEvent(ctx, ...)` | 上游 WS 事件观察（schema ≥4） |
| 25 | `ThinkingApplier` | `ThinkingApplier` | `:1430` | `ApplyThinking(...)` | 把 thinking 配置写进 payload |
| 26 | `UsagePlugin` | `UsagePlugin` | `:1438-1440` | `HandleUsage(ctx, UsageRecord)` | 每次请求完成 |
| 27 | `CommandLinePlugin` | `CommandLinePlugin` | `:1443-1447` | `RegisterCommandLine` / `ExecuteCommandLine` | 自定义 CLI 子命令 |
| 28 | `ManagementAPI` | `ManagementAPI` | `:1515-1517` | `RegisterManagement(ctx, ManagementRegistrationRequest) (ManagementRegistrationResponse, error)` | 插件注册管理路由/资源 |
| 29 | `QuotaProvider` | `QuotaProvider` | `:1689-1698` | `Identifier` / `DescribeQuota` / `FetchQuota` / `ResetQuota` | 管理端额度查询 |

注册校验（必填项）：`internal/pluginhost/host.go:1191-1199` —— `Metadata.Name` / `Version` / `Author` / `GitHubRepository` 四个非空，且至少一个 Capability 非 nil。

## B.2 ★ 关键问题 1：插件能否读到「发给上游的 `/models`」或「CPA 聚合的 `/v1/models`」？

### 结论 1a：`/v1/models`（CPA 聚合出的、客户端可见的）**能读到并能改**

证据链（三跳）：

1. `/v1/models` 路由 → `unifiedModelsHandler` → 各 handler 收尾统一走 `WriteModelListResponse`：
   `internal/api/server_routes.go:618-657`（`unifiedModelsHandler`）、`:701`（`s.handlers.WriteModelListResponse`）
2. `WriteModelListResponse` **就是**插件 `ResponseInterceptor` 的调用点：
   `sdk/api/handlers/handlers_interceptors.go:677-734`
   ```go
   // WriteModelListResponse serializes the model-list payload, applies plugin response interceptors
   // if a plugin host is configured, and writes the resulting headers and body to the Gin context.
   func (h *BaseAPIHandler) WriteModelListResponse(c *gin.Context, sourceFormat string, payload any) {
       ...
       resp := interceptResponse(ctx, host, pluginapi.ResponseInterceptRequest{
           SourceFormat: sourceFormat, Model: "", RequestedModel: "", Stream: false,
           RequestHeaders: reqHeaders, ResponseHeaders: ..., Body: cloneBytes(body),
           StatusCode: http.StatusOK, ...}, "")
       if len(resp.Body) > 0 { body = cloneBytes(resp.Body) }
   ```
3. 分发聚合：`internal/pluginhost/adapters_interceptors.go:325-352`（`InterceptResponse` 遍历所有 activeRecords 串行链式改写）

**但它作为「数据源」几乎无价值**：OpenAI 形态的 `/v1/models` 只输出 4 个字段：
`internal/.../openai_handlers.go`（`sdk/api/handlers/openai/openai_handlers.go:81-104`）—— 注释直说 “Filter to only include the 4 required fields: `id`, `object`, `created`, `owned_by`”。
它**不含** `max-context-length`、`input-modalities`、`thinking.levels` 等我们想同步的参数。

### 结论 1b：CPA 发给上游的 `/models`，**根本不存在**（对 openai-compatibility 通道）

openai-compatibility 渠道的模型清单 **只来自 `config.yaml`**，没有「向上游拉取」的运行时环节：
- 配置结构：`internal/config/config_types.go:1019-1057`（`OpenAICompatibilityModel`：`name`/`alias`/`display-name`/`max-context-length`/`force-mapping`/`image`/`input-modalities`/`output-modalities`/`is-compat`/`use-max-completion-tokens`/`thinking`）
- 客户端看到的目录里 openai-compat 模型的身份来自配置：`internal/registry/model_registry.go:104` 注释 “array (e.g., `openai-compatibility.*.models[]`, `*-api-key.models[]`)”
- 全套源码中**没有**对 openai-compatibility 上游 `/models` 的 executor 侧调用（`internal/runtime/executor/openai_compat_executor.go` 只处理 chat/images 执行）。全仓扫 `"/models"` 的出站调用只剩 `cmd/fetch_codex_models/main.go:46` 与 `cmd/fetch_antigravity_models/main.go`，它们都是**离线维护 CLI**，用途是重新生成 `internal/registry/models/*.json` 静态目录，且只覆盖 Codex / Antigravity 两家，与通用 openai-compatibility 无关。

> 换言之：**这个「上游有、CPA 没有」的信息缺口是真实存在的，且 CPA 自己不会填。** 这正是本项目的立项理由。
> 顺带一提：CPA 官方的「拉上游模型」实践是**离线 CLI 生成静态 JSON**，而不是运行时同步 —— 这本身也侧面支持用「外部工具」而非「运行时插件」来解决（见 C 路径 3）。

### 结论 1c：没有专门的「catalog 能力」

`sdk/pluginapi` 里没有 `ModelCatalog` capability。模型目录只能通过两条路介入：
- 出站方向：`ResponseInterceptor`（改客户端看到的列表）
- 入站方向：`ModelRegistrar` / `ModelProvider`（**给自己家的 provider 供模型**，不能给别人的渠道打补丁，见 A.6 与 `types.go:408-410`）

### 实现坑（必须提前知道）

`ResponseInterceptRequest` **没有 URL/Path 字段**（`sdk/pluginapi/types.go:1314-1328`）。插件无法可靠判定「这次拦截是不是 `/v1/models`」，只能靠启发式：
`Model == "" && RequestedModel == "" && Stream == false && StatusCode == 200 && OriginalRequest == nil && RequestBody == nil` + body 能解析成 `{"object":"list","data":[...]}`。
现存的 `model-sort` / `model-list-tweaker` / `models-cache-override` 都是这么干的（推断：由其覆盖 5~6 种客户端列表形态的自述反推），属于**已被生态验证但脆弱**的做法。

## B.3 ★ 关键问题 2：插件能否修改/持久化供应商配置？

### 2a host API：**不能**

host 暴露的方法**全部**在此（`sdk/pluginabi/types.go:118-138`），共 20 个：

```
host.http.do            host.http.do_stream      host.http.operation_open   host.http.cancel
host.http.stream_read   host.http.stream_close   host.model.execute        host.model.execute_stream
host.model.stream_read  host.model.stream_close  host.stream.emit          host.stream.close
host.log                host.auth.list           host.auth.get             host.auth.get_runtime
host.auth.save          host.auth.models         host.affinity.lookup      host.routing.reset_cooldown
```

**没有 config 读、config 写、config 路径、管理密钥获取。** 唯一与配置沾边的是 `HostConfigSummary`（`sdk/pluginapi/types.go:235-247`），只暴露 `AuthDir` / `ProxyURL` / `ForceModelPrefix` / `OAuthModelAlias` / `ExcludedModels` 的**只读摘要**，且只在 `StaticModelRequest` / `AuthModelRequest` / `AuthLoginStartRequest` 里出现。

`host.auth.save`（`types.go:890-898`）只能写**凭据 auth 文件**，写不了 `config.yaml`。

### 2b 官方管理 API：**能，这是唯一通路**

| 通道 | 路由 | 语义 | 源码 |
| --- | --- | --- | --- |
| 整文件替换 | `PUT /v0/management/config.yaml` | 读全量 body → `yaml.Unmarshal` → 写临时文件 → `LoadConfigOptional` 校验 → 原子写 → reload 进内存 | `internal/api/server_management.go:33`；实现 `internal/api/handlers/management/config_basic.go:129-176` |
| **定向改渠道模型（推荐）** | `PUT / PATCH /v0/management/openai-compatibility` | PUT 全量替换列表；PATCH 按 `{name,index,value:{models:[...]}}` 只改一个渠道的 `models` | `internal/api/server_management.go:155-158`；PATCH 结构体见 `internal/api/handlers/management/config_lists.go:906-928`（`Models *[]config.OpenAICompatibilityModel`） |
| v8 路径式 | `PUT/PATCH/DELETE /v8/management/config/*path` | 按 v8 YAML 树路径改写，**带 `If-Match` ETag 乐观锁**，保留注释 | `internal/api/server_management_v8.go:25-29`；`internal/api/handlers/management/config_v8.go:27-40` |

鉴权：`internal/api/handlers/management/handler.go:323-353`（`Authorization: Bearer <key>` 或 `X-Management-Key`），`:356-455`（`AuthenticateManagementKey`：远程需 `remote-management.allow-remote`；本地 `127.0.0.1` 走 `localPassword` 或 env secret 或 bcrypt 比对 `remote-management.secret-key`）。默认 5 次失败封 30 分钟（`:360-362`）。

**插件怎么拿到这把钥匙**——只有一条现实可行的路，且已有两个插件在这么做：
管理路由本身就在 `mgmt.Middleware()` 后面（`internal/api/server_management.go:28-29`），所以插件处理 `management.handle` 时，`ManagementRequest.Headers` 里**就带着调用方传进来的管理密钥**（`sdk/pluginapi/types.go:1569-1580`）。插件把它留在内存里，再回打上面的写路由。
先例：`cpa-account-config-manager` README `:153`「插件会记住最近一次管理请求带来的密钥，仅存内存、不落盘、不记日志」；`model-list-tweaker` 则是让操作者把 `management_key` 直接写进 `plugins.configs`。

### 2c 写完之后会不会生效？

会，热重载链路完整：
- 文件 watcher 对 openai-compat 做 diff：`internal/watcher/diff/config_diff.go:510`（`DiffOpenAICompatibility`）、`internal/watcher/diff/openai_compat.go:13-107`
- 模型指纹变化触发模型注册表刷新：`internal/watcher/synthesizer/config.go:339`、`:381`（`diff.ComputeOpenAICompatModelsHash`）→ `internal/modelconfig/model_hash.go:15`

## B.4 ★ 关键问题 3：插件能否注册自己的管理 API / 管理页面？

**能。** 接口：`sdk/pluginapi/types.go:1515-1517`

```go
type ManagementAPI interface {
    RegisterManagement(context.Context, ManagementRegistrationRequest) (ManagementRegistrationResponse, error)
}
```

请求/响应（`types.go:1519-1561`）：

```go
type ManagementRegistrationRequest struct {
    Plugin           Metadata
    BasePath         string // 唯一允许的前缀
    ResourceBasePath string // 浏览器可导航资源前缀
}
type ManagementRegistrationResponse struct {
    Routes    []ManagementRoute    // 管理 API
    Resources []ResourceRoute      // 浏览器资源（/v0/resource/plugins/<pluginID>/）
}
type ManagementRoute struct {
    Method, Path string      // Path 是 /v0/management/ 下的相对路径
    Menu, Description string // 旧式资源菜单
    Handler ManagementHandler
}
```

要点：
- 前缀常量：`/v0/management` 与 `/v0/resource/plugins`（`internal/pluginhost/management.go:18-20`）
- 路由冲突处理：与宿主内置路由冲突 **直接跳过**；与更高优先级插件冲突也跳过 —— `internal/pluginhost/management.go:71-78`
  所以**不能**抢占 `config.yaml`、`openai-compatibility` 这些保留路由。
- 鉴权：管理路由走 `s.mgmt.Middleware()`（密钥校验）；**资源路由不做管理鉴权**（`types.go:1559` “Resource requests are not management-authenticated”）—— 页面里需要自己做一次性密钥传递或只放静态 UI。
- 注册时机：宿主在启动/插件变更后调用 `RegisterManagementRoutes`（`internal/api/server_management.go:230-236`），并在 no-route 时按需分发（`internal/api/server_management.go:255-286`，其中 `:275` 调用 `s.mgmt.Middleware()` 完成密钥校验）。

**真实例子（registry 里带 Management tag 且确认注册路由的）**：

| 插件 | 路由前缀 | 证据 |
| --- | --- | --- |
| `cpa-account-config-manager` | `/v0/management/plugins/cpa-account-config-manager` | README `:136`、`:154` 逐条列出 `GET /codex/overview`、`POST /opencode/bind`、`PUT /opencode/model-control` 等，并强调「所有路由均为固定路径并要求 Management Key」 |
| `api-key-manager` | `/v0/management/plugins/api-key-manager/config` | README `:23` |
| `cpa-model-dock` | `$DOCK_API`（其自有前缀） | README `:182-188` 用 `POST "$DOCK_API/aliases/check"` |
| `models-cache-override` | `/v0/resource/plugins/models-cache-override/status` | README `:15`（注意这是 **Resource** 而非 Management 路由，无密钥鉴权） |

## B.5 ★ 关键问题 4：插件能否主动发 HTTP 到上游？

**能，两条路，都可用：**

1. **host 提供的 HTTP 客户端**：`HostHTTPClient`（`sdk/pluginapi/types.go:709-715`）
   ```go
   type HostHTTPClient interface {
       Do(context.Context, HTTPRequest) (HTTPResponse, error)
       DoStream(context.Context, HTTPRequest) (HTTPStreamResponse, error)
   }
   ```
   注入位置：`AuthModelRequest.HTTPClient`（`:460-461`）、`AuthLoginStartRequest.HTTPClient`（`:324-325`），字段标 `json:"-"`（本地注入，不过 RPC）。
   实现：`internal/pluginhost/host_callbacks.go:200-214`（`host.http.do` → `h.newHTTPClientWithProxy(...).Do(...)`）。
   **未发现目标地址白名单/黑名单限制** —— 理论上可打 `http://127.0.0.1:8317`。前提是必须在有效的 host 回调上下文里（`callbackContextPluginID`，`:196-198`），即要在某个 host 回调内调用。
   设计意图：`types.go:710-711` “Plugin executors must use this client for upstream calls so request-log can capture…”。

2. **插件内部直接用 `net/http`**：插件是 Go `c-shared` 库，同进程运行，`net/http` 完全可用。`model-list-tweaker` 调管理 API、`cpa-model-dock` 做 health check 都是这类（推断：其 README 未提 host 方法，且示例用 curl 打自有 API）。

> 建议：走 host 客户端（可被 request-log 观测）；需要打 CPA 自身管理端口时也用它，避免多套连接池与代理策略。

**补充：读「现有模型清单」也有现成 host 通道**
`host.auth.list` / `host.auth.get` / `host.auth.models`（`sdk/pluginabi/types.go:131-135`），对应 `HostAuthModelsRequest` / `HostAuthModels`（`sdk/pluginapi/types.go:906-929`）。
`cpa-models-speed-test` README `:10` 就是靠这两个回调列出「所有上游账号与模型」——**这是插件侧读取当前 catalog 而不必解析 config.yaml 的正路**。
注意：`host.auth.*` 面向的是**凭据（auth）**，对 `api-keys.openai-compatibility` 这种「配置里写死模型」的渠道覆盖程度需实测（推断：其 `models` 返回可能来自 provider 自己的模型表，未必包含 config 里手写的 models 数组）。

## B.6 v7 → v8 的 ABI 变化

| 项 | v7.1.70 | v8.0.47 | 结论 |
| --- | --- | --- | --- |
| module path | `github.com/router-for-me/CLIProxyAPI/v7` | `.../v8`（`go.mod:1`） | **改 import 才能重新编译** |
| `pluginabi.ABIVersion` | `1` | `1`（`sdk/pluginabi/types.go:7`） | 不变 |
| `pluginabi.SchemaVersion` | `1` | `8`（`sdk/pluginabi/types.go:32`） | v7 插件上报 1 |
| 加载门槛 | — | `client.api.abi_version != 1` → 拒绝（`internal/pluginhost/loader_unix.go:166-169`） | **v7 的 `.so` 能过这一关** |
| schema 门槛 | — | `resp.SchemaVersion > 8` → 拒绝（`internal/pluginhost/rpc_client.go:109-110`） | **schema=1 ≤ 8，通过** |

**结论：privacyfilter 的 v7-built `.so` 在 v8 上应当能加载**，但会按 schema v1 语义运行，代价是：
- 管理响应 JSON 字符串被 HTML 转义（`internal/pluginhost/management.go:363`，`schemaVersion < SchemaVersionRawManagementResponse` 即 <6 时转义）
- 流式每个 payload chunk 仍重复携带 `OriginalRequest` / `RequestBody` / `HistoryChunks`（`internal/pluginhost/adapters_interceptors.go:377`、`:387`、`:440`、`:473`）—— 大 prompt 场景显著放大 cgo/JSON 开销
- 不能用 `request.patch_body_before/after` 的零拷贝字节区间改写（`.../adapters_interceptors.go:506`，需 schema ≥8）

**线上现实**：`/srv/cliproxyapi/plugins/linux/amd64/privacyfilter-v0.2.0.so.orig_bak` 存在（已被改名停用），当前只启用 `antigravity-priority` 与 `keeper`，与此无关。

**新插件建议**：直接用 v8 SDK 编译、`SchemaVersion: 8`。
**兼容性核对**：v7 与 v8 的 `ResponseInterceptRequest` 只差一个 `RequestID` 字段（v7 `:827-839` vs v8 `:1314-1328`），且 RPC 线格式是**嵌入结构体 + Go 字段名**（`internal/pluginhost/rpc_schema.go:114-117`：`type rpcResponseInterceptRequest struct { pluginapi.ResponseInterceptRequest; HostCallbackID string }`），未知字段被 `encoding/json` 忽略 → **加字段是线兼容的**（推断：基于 json 默认行为 + 两版字段集合对比）。

**manifest / metadata 必填字段**（`internal/pluginhost/host.go:1191-1199`）：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `Metadata.Name` | ✅ | 空则整插件被丢弃 |
| `Metadata.Version` | ✅ | |
| `Metadata.Author` | ✅ | |
| `Metadata.GitHubRepository` | ✅ | |
| `Metadata.Logo` | ❌ | 管理端展示用 |
| `Metadata.ConfigFields[]` | ❌ | 但**强烈建议填**：管理端据此渲染配置表单。类型见 `types.go:40-71`（string/number/integer/boolean/enum/array/object），`Name` 对应 `plugins.configs.<pluginID>.<Name>` |
| `Capabilities` | ✅（至少 1 个非 nil） | |
| `SchemaVersion` | ⚠️ | 0 视为 legacy（`types.go:18-20`） |

插件实例配置（宿主侧）：`internal/config/config_types.go:44-51` —— `enabled`（**nil 归一化为 false**）、`priority`、`Raw`（保留完整原始 YAML 子树供插件自己解析）。线上 `config.yaml` 的 `plugins.configs.<id>` 即此结构（已只读核对 `antigravity-priority` / `keeper`）。

---

# C. 实现路径建议

前置事实（决定取舍的三条硬约束）：
- **C-1** 插件拿不到上游 `/models` 的「输入」：openai-compat 的 base-url 与 api-key 都在 `config.yaml`，插件只能通过管理 API 读到（要么操作者把密钥写进 `plugins.configs`，要么像 `cpa-account-config-manager` 那样从入站管理请求里截获）。
- **C-2** 插件写配置必须绕管理 API，且**整条链依赖一把可读可写的管理密钥**——这是本方案最大的安全面。
- **C-3** 要求「必须经过完整测试再上生产」，而插件是 `cgo` 共享库，**改动即需重启进程**（`cpa-account-config-manager` README `:138` 明确「CPA 只会在插件市场安装之后重新加载原生插件」），线上有活跃流量。

## 路径 1：纯插件闭环（catalog 拦截 + 管理路由 + 回调管理 API）

| 项 | 内容 |
| --- | --- |
| 挂点 | `ResponseInterceptor`（可选，改客户端 catalog）+ `ManagementAPI`（提供「同步」按钮/状态页） |
| 数据流 | 操作者在插件页触发 → 插件用内存里的管理密钥 `GET /v0/management/config.yaml`（或 `GET /openai-compatibility`）拿到渠道 base-url/api-key → 用 `host.http.do` 打上游 `GET {base}/models` → 归一化 → 与现有 `models[]` 做 diff → `PATCH /v0/management/openai-compatibility` 只写变化项 |
| 写回方式 | **持久化**（管理 API → `config.yaml`），走 watcher 热重载生效 |
| 热重载影响 | 每次写触发一次配置重载 + 模型表刷新（`internal/watcher/synthesizer/config.go:339`）；写入频率高会造成反复 reload |
| 失败模式 | 管理密钥缺失/过期 → 全链失效且**静默**（管理鉴权 5 次失败封 30 分钟，`handler.go:359-360`）；`If-Match` 未用 → 并发写覆盖；上游 `/models` 字段口径不一（各家 `context_length` / `max_context_length` / `context_window`）导致误写 |
| 工作量 | **高**：cgo + 手写 ABI 样板（参考 `examples/plugin/management-api/go/main.go` 的 C 段，或 privacyfilter `abi.go`）+ 上游归一化 + diff/校验 + 管理页 UI + 需自建 fixture 上游做端到端 |
| 评价 | 能力上完全可行，**但把最敏感的管理密钥塞进插件进程，且首发即需重启生产**。不建议作为第一版。 |

## 路径 2：插件只做「拉取 + 合并 + 校验」，输出 patch

| 项 | 内容 |
| --- | --- |
| 挂点 | `ManagementAPI`（一个只读路由 `/v0/management/plugins/model-meta-sync/plan` 返回 diff JSON）+ 可选 `ResponseInterceptor`（仅用于展示，不改配置） |
| 数据流 | 插件读渠道配置 → 打上游 `/models` → 产出 **结构化 patch**（`{provider, added[], changed[], removed[], conflicts[]}`）→ 返回给调用方；**插件不落盘** |
| 写回方式 | 由外部脚本/人确认后，用管理 API 落盘（复用路径 3 的落盘器） |
| 热重载影响 | 插件本身零写入 → 零额外 reload |
| 失败模式 | patch 与落盘之间的时间窗内配置被改动 → 需要 revision/`If-Match` 保护（v8 `config_v8.go:36-40` 提供） |
| 工作量 | **中**：无落盘风险，但仍有 cgo/ABI/上游归一化成本，且**必须再做一套落盘器**——总工作量 ≥ 路径 3 |
| 评价 | 作为路径 3 的「UI 前端」很合适（操作者在管理面板里预览 diff），作为独立方案则性价比低。 |

## 路径 3：不写插件，管理 API + 定时任务的外部同步器 ★ 推荐

| 项 | 内容 |
| --- | --- |
| 挂点 | **无插件**。外部进程（cron / systemd timer / supervisord 常驻）跑在 CPA 主机本机 |
| 数据流 | ① `GET /v0/management/openai-compatibility` 或 `GET /v0/management/config.yaml` 拿渠道与密钥 → ② 逐渠道打上游 `GET {base}/models`（**外部进程，直连，不经 CPA**）→ ③ 按 flavor 归一化字段（同 A.4 观察：各家 `/models` 口径不同）→ ④ 与现有 `models[]` 做三方 diff（保持人工手改的 `alias` 不被覆盖）→ ⑤ `PATCH /v0/management/openai-compatibility {name,index,value:{models:[…]}}` 只写变化 → ⑥ 校验 `GET /model-definitions/<channel>` 或再读一次配置确认 |
| 写回方式 | **持久化**，且是**最小面写入**（PATCH 单渠道 models，不动其他行/其他字段） |
| 热重载影响 | 与路径 1 同源，但**可批处理**：一次 PATCH 写完全部渠道的一个渠道，避免 N 次 reload；且 cron 频率可控（如 6h） |
| 失败模式 | 上游 4xx/超时 → 跳过该渠道、保留上次结果（fail-open 不写）；权限不足 → 返回 403 明确可见而非静默；并发人工改配置 → 用 v8 `If-Match` 或读后比对 revision（`config_v8.go:36-40`）；**最大风险是上游字段误读导致写坏 `max-context-length`** → 用 dry-run + diff 报表 + `--apply` 双闸 |
| 工作量 | **低**：一个 Go/Python 脚本 + 测试夹具（假上游 + 假 CPA 管理端）+ dry-run 报表。**零 cgo、零 ABI、零重启**，可先在 `/tmp` 或测试实例上跑通 |
| 评价 | 命中 C-1/C-2/C-3 全部约束：不需要插件持管理密钥（同步器自己就是管理客户端，密钥可放环境变量/systemd `EnvironmentFile` 0600）；改配置不改二进制，**无重启**；测试门槛最低（可完全在 fixture 上验证，再对生产只读跑 `--dry-run` 观察 N 天）。 |

## 排序与推荐

**推荐排序：路径 3 ＞ 路径 2 ＞ 路径 1。**

**选路径 3 的理由（逐条对应需求）**
1. **无重启**：插件是 `.so`，装上/改版都要重载原生插件（C-3）。同步器是独立进程，改代码不影响 CPA。
2. **密钥面最小**：路径 1 必须让 CPA 进程内的 `.so` 长期持有管理密钥（即便是内存态），任何插件漏洞都等于管理面失守。同步器把密钥关在 systemd unit 里，`chmod 600` 的 `EnvironmentFile`，且只能通过 CPA 的 403/401 边界暴露失败，不暴露密钥。
3. **测试可完整覆盖**：路径 3 的每个环节都能在本地 fixture 复现（假上游 `/models` 各种 flavor + 假管理端校验 PATCH 体）。路径 1 的 cgo 边界、ABI 版本、schema 语义只能靠「真的装到 CPA 上」验证，与「必须经过完整测试再上生产」冲突。
4. **可灰度**：先 `--dry-run` 只出报表，跑够周期确认 diff 稳定后再开 `--apply`；再退一步可只对单个渠道启用白名单。
5. **复用成熟通道**：`PATCH /v0/management/openai-compatibility` 是官方一等公民（`server_management.go:157`），已被 `cpa-account-config-manager` 在生产验证过（其 README `:153`）。

**什么情况下值得升级到路径 1/2**：若后续需要「操作者在 CPA 管理面板里点一下就同步并预览 diff」，则用**路径 2 的 UI + 路径 3 的落盘器**组合（插件只读、只出 patch）。这是唯一我认为值得引入插件的形态。

**不建议**：让插件自己写配置（路径 1 的写回环节）。收益是「少一个进程」，代价是「管理密钥进 cgo 库 + 生产重启 + 不可灰度」。

---

## 附录 A：证据索引（path:line 速查）

| 事实 | 位置 |
| --- | --- |
| Capabilities 26 字段全表 | `sdk/pluginapi/types.go:74-162` |
| ABIVersion=1 / SchemaVersion=8 | `sdk/pluginabi/types.go:7` / `:32` |
| schema 演进说明（v6/v7/v8） | `sdk/pluginabi/types.go:9-53` |
| host 方法清单（20 个） | `sdk/pluginabi/types.go:118-138` |
| `/v1/models` 走插件拦截 | `sdk/api/handlers/handlers_interceptors.go:677-734` |
| `/v1/models` 只输出 4 字段 | `sdk/api/handlers/openai/openai_handlers.go:81-104` |
| 拦截器串联执行 | `internal/pluginhost/adapters_interceptors.go:325-352` |
| 管理路由组 + 密钥中间件 | `internal/api/server_management.go:28-29` |
| openai-compatibility 四个写路由 | `internal/api/server_management.go:155-158` |
| `PUT /config.yaml` 实现 | `internal/api/handlers/management/config_basic.go:129-176` |
| `PATCH` 渠道 models 结构 | `internal/api/handlers/management/config_lists.go:906-928` |
| v8 路径式配置 + If-Match | `internal/api/server_management_v8.go:25-29`；`config_v8.go:36-40` |
| 管理密钥校验 + 封禁 | `internal/api/handlers/management/handler.go:323-455` |
| 插件管理路由注册/冲突跳过 | `internal/pluginhost/management.go:40-92` |
| 管理路由前缀常量 | `internal/pluginhost/management.go:18-20` |
| 资源路由免管理鉴权 | `sdk/pluginapi/types.go:1559` |
| 插件 metadata 必填校验 | `internal/pluginhost/host.go:1191-1199` |
| ABI 加载门槛 | `internal/pluginhost/loader_unix.go:166-169` |
| schema 版本门槛 | `internal/pluginhost/rpc_client.go:109-110` |
| host.http.do 实现（无目标限制） | `internal/pluginhost/host_callbacks.go:200-214` |
| ModelRegistrar 受凭据约束 | `sdk/pluginapi/types.go:408-410` |
| openai-compat 模型配置结构 | `internal/config/config_types.go:1019-1057` |
| 热重载 diff + 模型刷新 | `internal/watcher/diff/config_diff.go:510`；`internal/watcher/synthesizer/config.go:339`、`:381` |
| 插件实例配置结构 | `internal/config/config_types.go:44-51` |

## 附录 B：未验证项 / 已知缺口

1. **线上 CPA 的 ABI/schema 实况**：只读确认了 `.so` 文件名与两个启用插件，未 dump 其 `plugin.register` 返回的 `schema_version`（需要安装/调用，超出只读范围）。`antigravity-priority-v1.2.18` / `keeper-v0.1.0` 的实际 schema 未核。
2. **`cpa-secret-manager`、`embeddings-forward`、`cpa-subscription-tools`** 仅按 description 判为不相关，未读 README/源码。若需穷尽证明，可补核这 3 个。
3. **`ResponseInterceptRequest` 无 Path 字段** 导致 `/v1/models` 判定的启发式：这是从三个现存插件的行为反推的（推断），未逐行读它们的源码确认具体判定条件。
4. **v7 `.so` 在 v8 实际加载**：结论基于 `ABIVersion`/`SchemaVersion` 常量与两道门槛，未做真机加载实验。若要落到 privacyfilter 复活，建议先在一台测试实例上试装。
5. **上游 `/models` 的字段口径**（`context_length` vs `max_context_length` vs `context_window`、`supported_parameters` 等）：属 models-differ 的职责范围，本文件未展开，仅在 C-1/路径 3 的第 ③ 步标注为「需按 flavor 归一化」。
6. 线上 `config.yaml` 的 `plugins.store-sources` 与 `plugins.dir` 未逐项读取（只读了 `plugins.configs`），如需精确核对插件加载目录可再查一次。
