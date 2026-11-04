# Magpie `/v1/models` ↔ CPA openai-compatibility 模型元数据字段级差异（实测）

- 采集时间：2026-10-30 12:38–12:49 (+08:00)
- 采集者：teammate `models-differ`（只读侦察；未改 CPA config.yaml、未重启任何服务、未动 Magpie 配置）
- Magpie：`build-host` / build-host，原生安装 `magpie 0.1.2121`，二进制 `/srv/magpie/magpie`（2026-10-30 10:03），网关 `198.51.100.10:3425`，公网映射 `http://192.0.2.10:7509`，配置目录 `/srv/magpie/config/magpie/`
- CPA：`cpa-host`，`/srv/cliproxyapi/cli-proxy-api` 8.0.47（commit `90c447d`），`config-version: 8`，监听 `0.0.0.0:8317`，pid 2192521（进程直跑，`systemctl status cliproxyapi` 无对应单元，`journalctl -u cliproxyapi` 返回 “No entries”；日志走 `/srv/cliproxyapi/logs/main.log`）
- Magpie 源码：`/tmp/magpie-repo`，已 `git fetch --depth=1 origin HEAD` → `FETCH_HEAD = 8070484`（线上二进制与之行为一致，见 §1.4 的版本差异说明）
- CPA 源码：`/tmp/cpa-src` = `90c447d`（与线上 8.0.47 同 commit）

---

## 0. 一句话结论

Magpie 的 `/v1/models` 比 CPA 的模型条目**多出 6 类字段、少 1 类字段**：Magpie 独有的 `max_output_tokens / native_endpoints / pricing / service_tiers / reasoning_levels(平铺) / magpie_label` 在 CPA 的 `OpenAICompatibilityModel` 里**没有任何落点**；CPA 独有的 `image / force-mapping / is-compat / use-max-completion-tokens` 在 Magpie 侧**没有对应信息**。可自动同步的只有 4 个字段（`display-name / max-context-length / input-modalities / thinking.levels`），而现有 12 条配置里 `thinking.levels` 有 11 条与上游不一致、`max-context-length` 有 3 条不一致——这 12 条元数据是手工/模型猜的，不是同步来的。

---

## 1. Magpie `/v1/models` 真实字段清单

### 1.1 顶层响应结构

```json
{
  "data": [ ... 34 条 ... ],
  "first_id": "auto/deepseek-v4-flash-0731",
  "has_more": false,
  "last_id": "MiniMax-M3",
  "object": "list"
}
```

- 响应体 22,119 字节，`Content-Type: application/json`，`Transfer-Encoding: chunked`，HTTP 200。
- `object:"list"` + `first_id/last_id/has_more` 是分页**占位字段**：`has_more` 恒 `false`。
- **不分页**：实测 `?limit=2`、`?offset=32`、`?page=2`、`?after=auto/kimi-k3`、`?limit=5&after=...` 全部返回同样的 34 条、同样 `first_id`、`has_more=false`（查询参数被忽略）。源码 `gateway.go:1322` 直接拼 `map[object,data,has_more:false]`，没有游标读取。
- **需要鉴权**：无 key → `401`，body 为 `{"error":{"code":null,"message":"no API key was sent: send a magpie gateway key as Authorization: Bearer <key> (or x-api-key)",...}}`；`Authorization: Bearer <key>` 与 `x-api-key: <key>` 均可（都返回 200）；错 key → 401。
- **同一 id 不重复**：34 条 id 全部唯一（`len(set(ids)) == 34`），无一条重复 id。注意“同模型两条不同 id”是存在的（见 §1.5）。
- **响应形状随 User-Agent 变化**（对同步工具是坑）：
  | UA | 条数 | 字段差异 |
  |---|---|---|
  | `curl/8.5.0`（默认/中性） | 34 | 21 个字段（下表） |
  | `cursor-agent-exec/3.23.12` | 34（id 集合相同） | 额外 `api_types`、`capabilities`（32,158 字节） |
  | `grok-shell/1.0` | 34 | 与默认完全一致 |
  | `claude-desktop/1.2.3` | **32（完全不同的 id 集合）** | 变成 `anthropic/magpie-<num>`、`mythos-magpie-<num>` 的桌面专用目录，且**丢弃** `magpie_label / pricing / service_tiers / supported_parameters / tool_call` |
  - 依据：`gateway.go:1206` 的 `models()` 按 `agentOf(r)` 分支（`claude-desktop` / `ScienceID` / `cursor-local` 各自成体系），`toolsTold`（`gateway.go:1077`）等也只在通用分支调用。
  - **结论：同步器必须用中性 UA（如 curl 默认）取全量字段；否则会拿到桌面端特化目录。**

### 1.2 字段清单（默认 UA，34 条中出现次数）

| 字段 | 类型 | 出现 | 语义 | 证据 |
|---|---|---|---|---|
| `id` | string | 34/34 | 唯一模型 id，**寻址用**；`auto/<model>` = 自动发现的路由组，`<provider>/<model>` = 直连通道，裸名 = 附加的 bare id 行 | `gateway.go:1016` |
| `object` | string | 34/34 | 恒 `"model"` | 同上 |
| `type` | string | 34/34 | 恒 `"model"`（`magpie_label`/`kind` 分支另有 `"image"/"video"/"decision"`） | 同上 |
| `created` | int | 34/34 | **恒 `0`**（不是真时间戳） | 同上 |
| `created_at` | string | 34/34 | **恒 `"2025-01-01T00:00:00Z"`** | 同上 |
| `owned_by` | string | 34/34 | 上游 provider id（`modelscope`/`codex`/`copilot`/`systalyze`/`groq`/`minimax-code`/`qoder`/`autoclaw`/`cloudflare-workers-ai`/`kiro`），**路由组也填首个成员的 provider，不是 “auto”** | 同上 |
| `display_name` | string | 34/34 | 展示名（`PlainName()`），与 id 末段大小写/空格不同（32/34 条不同） | 同上 |
| `magpie_label` | string | 34/34 | `"<名字> · <provider 名 或 'routing group'>"`；用于在同一列表里区分同名模型 | `gateway.go:1280` |
| `reasoning` | bool | 34/34 | 是否思考模型（有档位或标记 Reasoning） | `gateway.go:1016` |
| `reasoning_levels` | []string | 34/34 | **平铺**档位，无档位时 `[]`（如 `gemma-4-26b-a4b-it`、`glm-5`、`llama-3.2-11b`） | `gateway.go:1016` |
| `supported_reasoning_levels` | list of `{"effort": string}` | 34/34 | **同一份档位的对象化版本**（两套 schema 双写，历史原因：Catwalk 客户端读平铺、老客户端读对象） | `gateway.go:1016` |
| `context_window` | int | 34/34 | 上下文窗口（=`Entry.Context`） | `gateway.go:1036` |
| `context_length` | int | 34/34 | 同上，**三个字段同值** | `gateway.go:1036` |
| `max_input_tokens` | int | 34/34 | 同上 | `gateway.go:1036` |
| `max_output_tokens` | int | **32/34** | **最大输出**（不是上下文），并被**钳到 ≤ 上下文**（见 §3.1 证据链）；缺 2 条（`auto/qfmodel`、`auto/dfmodel`，qoder 通道未给输出上限） | `gateway.go:1041` + `provider/models.go:2946,2955` |
| `native_endpoints` | []string | **2/34** | 请求**原文直通**（不被翻译）的上游 API 列表；本机只有 `minimax-code/MiniMax-M3`、`minimax-code/MiniMax-M3.1-Flash` 带 `["/v1/messages"]` | `gateway.go:1047` |
| `modalities` | object `{input?: []string, output?: []string}` | **25/34** | 25 条有（23 条只有 `input`（`["text"]` 或 `["text","image"]`），2 条 minimax-code 有 `input+output`）；9 条**缺整个字段** | `gateway.go:1052-1062` |
| `pricing` | object `{prompt, completion, input_cache_read?, input_cache_write?}`（字符串数字，美元/token） | **24/34** | OpenRouter 风格价格；24 条有，键组合三种：`{prompt,completion,input_cache_read}`×12、`+input_cache_write`×9、`{prompt,completion}`×3；10 条无 | `gateway.go:1270` |
| `service_tiers` | list of `{id,name,description}` | **3/34** | 仅 Codex 通道的 `auto/gpt-6-luna`、`auto/gpt-5.6-terra`、`auto/gpt-5.6-luna`：`[{"id":"priority","name":"Fast","description":"1.5x speed, increased usage"}]` | `gateway.go:1266` |
| `tool_call` | bool | 34/34 | 恒 `true`（网关把所有 chat 模型都当能调工具） | `gateway.go:1077 toolsTold` |
| `supported_parameters` | []string | 34/34 | `["tools","tool_choice","reasoning"]` 33 条；`["tools","tool_choice"]` 1 条（`auto/llama-3.2-11b-vision-instruct`，`reasoning=false`） | `gateway.go:1077` |

非预期字段共 4 个：`magpie_label`、`pricing`、`service_tiers`、`supported_parameters`（另有 `reasoning_levels` 与 `supported_reasoning_levels` 的**双写**，以及 `context_window/context_length/max_input_tokens` 的**三写同值**）。

### 1.3 完整样本（无密钥，原样）

`auto/deepseek-v4-pro`（最完整：窗口/输出/价格/档位/工具）：

```json
{"context_length":1000000,"context_window":1000000,"created":0,"created_at":"2025-01-01T00:00:00Z",
 "display_name":"DeepSeek V4 Pro","id":"auto/deepseek-v4-pro","magpie_label":"DeepSeek V4 Pro · routing group",
 "max_input_tokens":1000000,"max_output_tokens":384000,"object":"model","owned_by":"modelscope",
 "pricing":{"completion":"0.00000198","input_cache_read":"0.000000022","prompt":"0.00000066"},
 "reasoning":true,"reasoning_levels":["low","high","max"],
 "supported_parameters":["tools","tool_choice","reasoning"],
 "supported_reasoning_levels":[{"effort":"low"},{"effort":"high"},{"effort":"max"}],
 "tool_call":true,"type":"model"}
```

`minimax-code/MiniMax-M3`（唯一带 `native_endpoints` + 输出模态的一类）：

```json
{"context_length":1048576,"context_window":1048576,"created":0,"created_at":"2025-01-01T00:00:00Z",
 "display_name":"M3","id":"minimax-code/MiniMax-M3",
 "magpie_label":"M3 · MiniMax Code","max_input_tokens":1048576,"max_output_tokens":524288,
 "modalities":{"input":["text","image"],"output":["text"]},
 "native_endpoints":["/v1/messages"],"object":"model","owned_by":"minimax-code",
 "pricing":{"completion":"0.0000012","input_cache_read":"0.00000006","prompt":"0.0000003"},
 "reasoning":true,"reasoning_levels":["none","high"],
 "supported_parameters":["tools","tool_choice","reasoning"],
 "supported_reasoning_levels":[{"effort":"none"},{"effort":"high"}],
 "tool_call":true,"type":"model"}
```

### 1.4 版本对齐说明（重要）

- 本地 clone 原为 `ef155a5c`（10-28）的 `modelObject` **不含** `tool_call / supported_parameters`；实测响应有这两个字段。
- 已 `git fetch --depth=1` 到 `8070484`，其 `gateway.go:1077 toolsTold()` 正好生成这两个字段，与线上完全吻合。
- **本报告的行号引用统一用 `8070484`（= 线上 0.1.2121 的行为）**。

### 1.5 id 家族（决定了 CPA 里 `name` 只能填哪些）

| 家族 | 条数 | 例子 | 说明 |
|---|---|---|---|
| `auto/<model>` | 30 | `auto/kimi-k3`、`auto/dfmodel` | **自动发现的路由组**（model 名被 ≥1 家 unlisted 通道同时提供时聚合；`magpie_label` 后缀 `routing group`）。对外只用这个 id，成员 provider 前缀 id **不出现**（`internal/provider/autoref.go:36 const AutoPrefix = "auto/"`） |
| `<provider>/<model>` | 2 | `minimax-code/MiniMax-M3` | **带前缀直连通道**。只有 `minimax-code` 这一家不是 `unlisted`（其余 22 家 `unlisted:true`），所以全机只有这 2 条前缀 id（`provider/models.go:2516 Catalog()` + `providers.json`） |
| 裸名 | 2 | `MiniMax-M3`、`MiniMax-M3.1-Flash` | `settings.json` 里 `bareModelIDs: true` 追加的 bare id 行（`provider/bare.go:50 BareEntries`），指向同一路由组，**但缺 `pricing`/`native_endpoints`** |

- **可寻址性实测**：`GET /v1/models/<url-encoded id>` 对列表内的 id 返回 200（如 `auto%2Fkimi-k3`、`auto%2Fauto`、`MiniMax-M3`），对列表外的 id 一律 `404 {"error":{...,"message":"unknown model modelscope/deepseek-v4-pro","type":"not_found_error"}}`。实测 404 的：`modelscope/deepseek-v4-pro`、`codex/gpt-5.6-sol`、`copilot/gpt-5.6-sol`、`systalyze/kimi-k3`、`kimi-k3`（小写裸名）。
- **因此 CPA 的 `name` 字段必须严格取这 34 个 id 之一**，不能凭 provider 名前缀拼。
- `?format=text`（源码 `models()` 末尾）可用：返回 34 行纯 id，便于对比。

---

## 2. CPA 侧模型条目结构

### 2.1 配置位置（与常见文档不同）

`config-version: 8` 的布局下，Magpie 供应商在 **`api-keys.openai-compatibility[8]`**（不是顶层 `openai-compatibility`）：

```
/api-keys/openai-compatibility  9 个 provider
  [8] name=Magpie  base-url=http://192.0.2.10:7509
      disabled=false  priority=9   keys=<1 个调用方 key，脱敏>   models=12
      ← 注意：没有设置 prefix
```

- 全部 provider 行字段实测只有：`base-url / disabled / keys / models / name / priority`（v8 里 `keys` 等价于老版的 `api-key-entries`；`api-keys.openai-compatibility[*]` 的共享字段白名单见 `internal/config/config_v8.go:384-388`，含 `priority/prefix/proxy-url/headers/models/excluded-models/disable-cooling/request-retry/request-scoped-errors`）。
- CPA 用的调用方 key 是 `caller-keys.json[0]`（`name=CLIPro…`，14 字符非 `sk-` 形态）；证据：CPA 日志里该 key 已整体脱敏为 `<redacted>`，与 key[0] 逐位一致（比对在只读会话中完成）。

### 2.2 12 条模型条目原文（脱敏后原样，无密钥）

| # | JSON |
|---|---|
| 0 | `{"alias":"kimi-k3","display-name":"Kimi K3","input-modalities":["text","image"],"max-context-length":1048576,"name":"auto/kimi-k3","output-modalities":["text"],"thinking":{"levels":["low","medium","high","max"]}}` |
| 1 | `{"alias":"minimax-m3.1-flash","display-name":"MiniMax-M3.1-Flash","input-modalities":["text","image"],"max-context-length":1048576,"name":"auto/minimax-m3.1-flash","output-modalities":["text"],"thinking":{"levels":["low","medium","high","xhigh","max"]}}` |
| 2 | `{"alias":"deepseek-v4-flash","display-name":"DeepSeek V4 Flash 0731","input-modalities":["text"],"max-context-length":1048576,"name":"auto/deepseek-v4-flash-0731","output-modalities":["text"],"thinking":{"levels":["low","medium","high","max"]}}` |
| 3 | `{"alias":"deepseek-flash","display-name":"DeepSeek V4.1 Flash","input-modalities":["text","image"],"max-context-length":1000000,"name":"auto/deepseek-v4.1-flash","output-modalities":["text"],"thinking":{"levels":["low","medium","high"]}}` |
| 4 | `{"alias":"deepseek-flash","display-name":"DeepSeek V4.1 Flash 1017","input-modalities":["text","image"],"max-context-length":1000000,"name":"auto/deepseek-v4.1-flash-1017","output-modalities":["text"],"thinking":{"levels":["low","medium","high"]}}` |
| 5 | `{"alias":"minimax-m3","display-name":"MiniMax-M3","input-modalities":["text","image"],"max-context-length":1048576,"name":"auto/minimax-m3","output-modalities":["text"],"thinking":{"levels":["none","low","medium","high"]}}` |
| 6 | `{"alias":"GLM-5.3","display-name":"GLM-5.3","input-modalities":["text"],"max-context-length":1048576,"name":"auto/glm-5.3","output-modalities":["text"],"thinking":{"levels":["low","medium","high"]}}` |
| 7 | `{"alias":"Kimi K3","display-name":"Kimi K3","input-modalities":["text","image"],"max-context-length":1048576,"name":"auto/kimi-k3","output-modalities":["text"],"thinking":{"levels":["low","medium","high","max"]}}` |
| 8 | `{"alias":"deepseek-v4-pro","display-name":"DeepSeek V4 Pro","input-modalities":["text"],"max-context-length":1000000,"name":"auto/deepseek-v4-pro","output-modalities":["text"],"thinking":{"levels":["low","medium","high","max"]}}` |
| 9 | `{"alias":"deepseek-v4-pro","display-name":"DeepSeek V4 Pro 0813","input-modalities":["text"],"max-context-length":1000000,"name":"auto/deepseek-v4-pro-0813","output-modalities":["text"],"thinking":{"levels":["low","medium","high","max"]}}` |
| 10 | `{"alias":"glm-5.3-flash","display-name":"GLM-5.3-Flash","input-modalities":["text","image"],"max-context-length":1048576,"name":"auto/glm-5.3-flash","output-modalities":["text"],"thinking":{"levels":["low","medium","high"]}}` |
| 11 | `{"alias":"deepseek-flash","display-name":"DeepSeek-Flash","input-modalities":["text","image"],"max-context-length":1000000,"name":"auto/dfmodel","output-modalities":["text"],"thinking":{"levels":["low","medium","high"]}}` |

- 每条实际只用 7 个键：`alias / display-name / input-modalities / max-context-length / name / output-modalities / thinking`。
- 一条模型条目可用字段全表（`internal/config/config_types.go:1019-1057`，`OpenAICompatibilityModel`）：

| YAML key | Go 类型 | 语义 | 当前 12 条是否使用 |
|---|---|---|---|
| `name` | string | **上游真实模型名**（这里= Magpie 的 id） | ✅ 12/12 |
| `alias` | string | 客户端看到的模型名（CPA 注册表的 model ID） | ✅ 12/12 |
| `display-name` | string | 目录展示名 | ✅ 12/12 |
| `max-context-length` | int | 覆盖向 Codex 客户端宣告的上下文窗口（`service_models.go:814-856` 落到 `ModelInfo.ContextLength/MaxContextLength`） | ✅ 12/12 |
| `input-modalities` | []string | chat/responses 输入模态（text/image/…） | ✅ 12/12 |
| `output-modalities` | []string | 输出模态（text/image） | ✅ 12/12 |
| `thinking` | `*registry.ThinkingSupport` | 见下 | ✅ 12/12 |
| `force-mapping` | bool | 把上游响应里的 model 字段改写回 alias | ❌ |
| `image` | bool | 该模型走 `/v1/images/generations`、`/v1/images/edits`（**不是**多模态输入开关） | ❌ |
| `is-compat` | bool | 保留 Claude thinking 块（跳过签名校验） | ❌ |
| `use-max-completion-tokens` | bool | 发 `max_completion_tokens` 而非 `max_tokens` | ❌ |

`thinking`（`internal/registry/model_registry.go:144-159`）：`min`/`max`（token 预算）、`zero-allowed`、`dynamic-allowed`、`disable-type`、`levels`（离散档位）。**12 条只用了 `levels`**。
- 语义细节：`thinking` 为 `nil` 时 CPA 默认注入 `levels: ["low","medium","high"]`（`service_models.go:873-875`）——这解释了现有条目里到处是 `medium` 的来源之一。
- `buildOpenAICompatibilityConfigModels`（`service_models.go:856`）**不做**静态 catalog 富化（不像 OAuth 通道会 `ResolveModelInfo`），所以 openai-compatibility 模型的元数据**完全由 config.yaml 决定**。

### 2.3 关键否定结论（结构体层面上）

1. **openai-compatibility 模型条目没有任何“最大输出 token”键**。`ModelInfo` 里虽有 `OutputTokenLimit`/`MaxCompletionTokens`（`model_registry.go:78` 附近），但赋值只发生在编译进二进制的静态 registry（`internal/registry/model_definitions.go` 等）与 antigravity/codex/devin 专用抓取器（`cmd/fetch_*_models`）；`config.yaml` 路径不设置它们。→ **Magpie 的 `max_output_tokens` 无处落地。**
2. **没有 `native_endpoints` 的对应物**。`is-compat` 只是保留 thinking 块，和“直通不翻译”无关。
3. **没有 `pricing`/`service_tiers`/`owned_by` 的 per-model 落点**（`owned_by` 在 CPA 侧恒为 provider 的 `name`，即 `"Magpie"`）。
4. CPA **没有**“从上游 `/v1/models` 自动拉取”的通用能力：`cmd/` 只有 `fetch_antigravity_models`、`fetch_codex_models`、`fetch_devin_models`（OAuth 通道专用）。→ 同步器必须外部实现。
5. 写回通道存在：管理 API v8 `GET/PUT/PATCH /v8/management/config/api-keys/openai-compatibility`（`internal/api/handlers/management/config_lists.go:906` 的 `PatchOpenAICompat`，`value.models` 整体替换）。

---

## 3. 字段映射表

### 3.1 先定死 `max_output_tokens` 的语义（证据链）

| 步骤 | 证据 |
|---|---|
| 字段由 `e.PublishedOutput()` 生成，而不是 `e.Context` | `gateway.go:1041`：`if out := e.PublishedOutput(); out > 0 { m["max_output_tokens"] = out }`；对比 `gateway.go:1036` 上下文三写用的是 `e.Context` |
| `PublishedOutput()` = `OutputWithin(e.Context, e.Output)` | `provider/models.go:2955` |
| `OutputWithin(window, output)`：`if window > 0 && output > window { return window }`，否则返回 `output` | `provider/models.go:2946-2951`；注释明确写 “the reply limit, kept within that window as agents are told it (#1438)” 与 “models.dev lists some models' output above the window … a client that reads both sizes its replies past what the window holds” |
| 实测可区分样本 | `auto/deepseek-v4-pro`：`context_length=1000000`、`max_output_tokens=384000`（输出 < 上下文，真输出上限）；`auto/deepseek-v4-flash-0731`：两者都 `1048576`（输出被钳到上下文，**不是**“上下文”语义，只是钳位结果） |
| 另一侧证据 | 2 条 qoder 模型（`auto/qfmodel`、`auto/dfmodel`）输出上限未知 → 字段**整体缺失**（不是 0），说明它来自 `e.Output` 而非窗口 |

**结论：`max_output_tokens` = 最大输出 token，且被 `min(输出, 上下文)` 钳过。** 不能当上下文用；也不能当“厂商原生输出上限”用（可能是钳位值）。

### 3.2 一行一行映射

| Magpie 字段 | CPA `OpenAICompatibilityModel` | 判定 | 说明 |
|---|---|---|---|
| `id` | `name` | **可直接映射**（且必须） | 列表里的 id 就是可寻址 id；`provider/<model>` 形式在本机只有 `minimax-code/*` 有效，其它前缀 404 |
| `display_name` | `display-name` | **可直接映射** | 12/12 现已一致 |
| `context_length`（=`context_window`=`max_input_tokens`） | `max-context-length` | **可直接映射** | 现 3 条写成 1000000（上游 1048576），需纠正 |
| `reasoning_levels`（或 `supported_reasoning_levels[].effort`） | `thinking.levels` | **可直接映射**（注意空数组） | 上游 `[]` 表示“无档位”，写 `levels: []` 会让客户端不显示推理档；现 11/12 被塞了 `medium` 等上游没有的档 |
| `modalities.input` | `input-modalities` | **可直接映射（有则填）** | 9 条上游缺该字段，CPA 侧**无从判断**，只能保守填 `["text"]` 或留空 |
| `modalities.output` | `output-modalities` | **需换算/保守** | 上游只有 2 条 minimax-code 给 `["text"]`；其余 32 条缺失 → 只能按 `["text"]` 兜底（推断，非实测值） |
| `max_output_tokens` | —— | **无法映射（CPA 没有）** | 唯一输出上限来源是编译内静态 registry；config 层写不进去 |
| `native_endpoints` | —— | **无法映射** | CPA 侧最接近的是 provider/模型级 `is-compat`（语义不同）；`image` 是画图端点开关，不是直通 API |
| `pricing` | —— | **无法映射** | CPA 模型条目无价格字段 |
| `service_tiers` | —— | **无法映射** | Codex “Fast 档”无落点 |
| `owned_by` | （`OwnedBy` 恒 = provider `name`） | **需换算** | CPA 里显示为 `"Magpie"`；Magpie 的 `owned_by` 是上游通道 id，**而且 `auto/*` 路由组的 `owned_by` 只是首个成员，不代表唯一通道** |
| `reasoning` (bool) | `thinking != nil`/`levels` | **需换算** | `reasoning=false` 时应不写 `thinking`（否则误报推理能力） |
| `tool_call` / `supported_parameters` | —— | **无法映射** | CPA 恒 `supports_tool_use=true`（cursor 分支的 `capabilities` 里）；配置层不暴露 |
| `magpie_label` | —— | **无法映射**（可用作 alias 生成素材） | 含 “· routing group / · <provider名>” 后缀，**不适合直接当 display-name** |
| `type` / `object` / `created` / `created_at` | —— | **无意义** | Magpie 端恒值；CPA 的 `created` 是启动时间（实测 `1793318206`） |
| —— | `alias` | **Magpie 无对应** | 必须自己定；这正是当前碰撞的根源（见 §4.2） |
| —— | `image` / `force-mapping` / `is-compat` / `use-max-completion-tokens` | **Magpie 无对应** | Magpie 只有“输入模态含 image”，与 `image`（画图端点）不是一回事 |
| —— | `thinking.min/max/zero-allowed/dynamic-allowed/disable-type` | **Magpie 无对应** | Magpie 只给离散档位，不给 token 预算区间 |

---

## 4. 12 条现有别名的覆盖率核算

### 4.1 逐条

| # | CPA `name` | `alias` | Magpie id 命中 | display | ctx | in-mod | out-mod | levels | 可自动填 |
|---|---|---|---|---|---|---|---|---|---|
| 0 | `auto/kimi-k3` | `kimi-k3` | ✔ | ✔ | ✔ | ✔ | ∅ | ✔ | 4/5 |
| 1 | `auto/minimax-m3.1-flash` | `minimax-m3.1-flash` | ✔ | ✔ | ✔ | ✔ | ∅ | ✔ | 4/5 |
| 2 | `auto/deepseek-v4-flash-0731` | `deepseek-v4-flash` | ✔ | ✔ | ✔ | ∅ | ∅ | ✔ | 3/5 |
| 3 | `auto/deepseek-v4.1-flash` | `deepseek-flash` | ✔ | ✔ | ✔ | ✔ | ∅ | ✔ | 4/5 |
| 4 | `auto/deepseek-v4.1-flash-1017` | `deepseek-flash` | ✔ | ✔ | ✔ | ✔ | ∅ | ✔ | 4/5 |
| 5 | `auto/minimax-m3` | `minimax-m3` | ✔ | ✔ | ✔ | ✔ | ∅ | ✔ | 4/5 |
| 6 | `auto/glm-5.3` | `GLM-5.3` | ✔ | ✔ | ✔ | ∅ | ∅ | ✔ | 3/5 |
| 7 | `auto/kimi-k3` | `Kimi K3` | ✔ | ✔ | ✔ | ✔ | ∅ | ✔ | 4/5 |
| 8 | `auto/deepseek-v4-pro` | `deepseek-v4-pro` | ✔ | ✔ | ✔ | ∅ | ∅ | ✔ | 3/5 |
| 9 | `auto/deepseek-v4-pro-0813` | `deepseek-v4-pro` | ✔ | ✔ | ✔ | ∅ | ∅ | ✔ | 3/5 |
| 10 | `auto/glm-5.3-flash` | `glm-5.3-flash` | ✔ | ✔ | ✔ | ✔ | ∅ | ✔ | 4/5 |
| 11 | `auto/dfmodel` | `deepseek-flash` | ✔ | ✔ | ✔ | ✔ | ∅ | ✔ | 4/5 |

（`✔` = 上游有该信息且可填；`∅` = 上游无该信息，只能缺省/推断）

**总覆盖**：12 条里 **12 条 name 全命中**（11 个 distinct id，`auto/kimi-k3` 被用两次）。

- 必然可自动写回的 3 个字段：`display-name`、`max-context-length`、`thinking.levels` = **12/12 条**
- 有条件可写：`input-modalities` = **8/12 条**（4 条上游缺 `modalities`：#2、#6、#8、#9）
- 完全不可写：`output-modalities` 实测值 **0/12**（上游 `auto/*` 全缺 output 模态，只能按 `["text"]` 兜底）、输出 token 上限 **0/12**（CPA 无字段）、`native_endpoints`/`pricing`/`service_tiers` **0/12**（CPA 无字段）
- 因此“全自动一条写满”的天花板是 **4 个字段**，其中 1 个不是每条都有上游值。

**当前值对不对（正确性核算）**：

| 字段 | 一致 | 不一致 | 不一致明细 |
|---|---|---|---|
| `display-name` | **12/12** | 0 | — |
| `max-context-length` | 9/12 | **3** | #3、#4、#9：CPA `1000000` vs 上游 `1048576` |
| `input-modalities` | 8/8 可核 | — | 4 条上游缺 `modalities`（#2、#6、#8、#9），无从核对 |
| `output-modalities` | 0 可核 | — | 上游 auto/\* 均无 `modalities.output`，全是 `["text"]` 推断 |
| `thinking.levels` | **1/12** | **11** | 见下 |

`thinking.levels` 逐条差异（上游 → CPA）：

```
#0  auto/kimi-k3                 [low,high,max]                  → [low,medium,high,max]        +medium
#2  auto/deepseek-v4-flash-0731  [low,high,max]                  → [low,medium,high,max]        +medium
#3  auto/deepseek-v4.1-flash     [low,high]                      → [low,medium,high]            +medium
#4  auto/deepseek-v4.1-flash-1017[none,low,high,max]             → [low,medium,high]            -none -max +medium
#5  auto/minimax-m3              [none,high]                     → [none,low,medium,high]       +low +medium
#6  auto/glm-5.3                 [low,high]                      → [low,medium,high]            +medium
#7  auto/kimi-k3                 [low,high,max]                  → [low,medium,high,max]        +medium
#8  auto/deepseek-v4-pro         [low,high,max]                  → [low,medium,high,max]        +medium
#9  auto/deepseek-v4-pro-0813    [none,low,high,max]             → [low,medium,high,max]        -none
#10 auto/glm-5.3-flash           [low,high]                      → [low,medium,high]            +medium
#11 auto/dfmodel                 [low,high,max]                  → [low,medium,high]            -max +medium
#1  auto/minimax-m3.1-flash      [low,medium,high,xhigh,max]     → 同                            ✔ 唯一一致
```

→ 11 条都为上游没有的 `medium`；3 条丢了 `max`；2 条丢了 `none`。

### 4.2 歧义与冲突案例（单列）

1. **同一上游两条 CPA 条目、不同 alias**：`auto/kimi-k3` 既是 `kimi-k3`（#0）又是 `Kimi K3`（#7），字段完全重复。同步器需要去重策略（按 name 唯一，保留一个）。
2. **同一 alias 对应不同上游**：`deepseek-flash` 被 3 条占用（#3 `auto/deepseek-v4.1-flash`、#4 `auto/deepseek-v4.1-flash-1017`、#11 `auto/dfmodel`）；`deepseek-v4-pro` 被 2 条占用（#8、#9）。CPA 注册表键是 **alias 原样字符串**（`registerClientLocked` `model_registry.go:519-530` + `addModelRegistration` `:747-773`：同 id 时 `existing.Info = clone` 覆盖展示，`Count++`），所以同名条目在 `/v1/models` 里会塌成 1 行。
3. **跨 provider 撞名（比内部撞名更严重）**：12 条 alias 里有 **8 条**与其它 provider 的 alias 撞车：

| alias | CPA 全局出现次数 | 撞车对象 |
|---|---|---|
| `deepseek-flash` | **5** | workbuddy2api(cn) `cn:deepseek-v4.1-flash`、workbuddy2api(go) `global:deepseek-v4.1-flash` + Magpie 3 条 |
| `deepseek-v4-pro` | 4 | 肖恩AI、workbuddy2api(cn) `cn:deepseek-v4-pro` + Magpie 2 条 |
| `glm-5.3-flash` | 4 | 肖恩AI `ZHIPU/GLM-5.3-Flash`、workbuddy2api(cn/ go) + Magpie |
| `kimi-k3` | 3 | workbuddy2api(cn) `cn:kimi-k3-1`、workbuddy2api(go) `global:kimi-k3` + Magpie |
| `minimax-m3.1-flash` | 3 | workbuddy2api(cn/ go) + Magpie |
| `GLM-5.3` / `Kimi K3` / `deepseek-v4-flash` / `minimax-m3` | 1（唯一） | — |

**实测后果**：CPA `/v1/models` 共 31 条，其中 `owned_by == "Magpie"` 只有 **4 条**：`deepseek-v4-flash`、`minimax-m3`、`Kimi K3`、`GLM-5.3` —— 正好是 4 个“全局唯一 alias”。其余 8 条虽然在注册表里（`Providers` 计数合并，日志可见 `provider=mixed model=deepseek-flash` 仍能路由到 Magpie），**但展示的元数据是别家 provider 的**。
→ 结论：**alias 不是装饰，是 CPA 的全局命名空间主键**。同步器必须解决命名（例如给 provider 设 `prefix` + `routing.force-model-prefix`，见 §5.3）。

4. **裸名语义不统一**：Magpie 侧 `MiniMax-M3`（裸）与 `minimax-code/MiniMax-M3`、`auto/minimax-m3` 是三个不同 id/不同内容（裸行无 `pricing`、`display_name` 也不同：`MiniMax-M3` vs `M3` vs `MiniMax-M3`）；CPA 侧 `minimax-m3`（#5）实际指 `auto/minimax-m3`。三套“同名不同物”极易混淆。
5. **`auto/` 前缀被去掉**：12 条 `name` 都带 `auto/`，但 12 条 `alias` 全都不带。这本身没错（alias 自由），但导致 `kimi-k3` 这类 alias 天然和“裸名通道”抢命名空间。

---

## 5. 上游模型清单规模与两种同步策略

### 5.1 规模实测

- Magpie `/v1/models` 共 **34 条**：`auto/*` **30** 条、带前缀直连 `minimax-code/*` **2** 条、裸 id **2** 条（`bareModelIDs: true` 追加）。
- 34 条按 `owned_by` 分布：modelscope 8、copilot 6、systalyze 5、minimax-code 4、codex 3、groq 2、qoder 2、autoclaw 2、cloudflare-workers-ai 1、kiro 1。
- 上游通道共 **23 个 provider**（`providers.json`），其中 **22 个 `unlisted: true`**，只有 `minimax-code` 是列出的 → 这就是“带前缀通道直连只有 2 条”的原因（`Catalog()` = `groupEntries` + 非 unlisted 条目，`provider/models.go:2516`）。
- 结论：**“通道直连”在本机几乎不存在**；30/34 都是路由组 id，`owned_by` 只反映组内首个成员。

### 5.2 策略 A：抓全量（34 条全写入 CPA）

- 收益：33 个新模型可用；未来 Magpie 增删模型可自动对齐；`/v1/models` 覆盖率 100%。
- 代价：
  - alias 命名空间压力剧增（`auto/auto`、`auto/qfmodel`、`auto/dfmodel`、`auto/zai_auto`、`auto/zai_auto-fast` 这些名字对用户毫无意义，必须重命名）；
  - 大量 id 的 `display_name` 与 id 语义不对应（`auto/qfmodel` → “Qwen3.8-Flash”、`auto/dfmodel` → “DeepSeek-Flash”、`auto/zai_auto` → “GLM Auto (路由)”，属于“代号”型模型，改名后无法从 id 推断）；
  - `pricing` 有 10 条缺失、`modalities` 有 9 条缺失、`max_output_tokens` 有 2 条缺失 → 元数据质量参差；
  - CPA 注册表是全局平铺命名空间，33 条新 alias 极易再撞其它 provider。

### 5.3 策略 B：只同步已配置的 12 条（当前形态的维护化）

- 收益：命名可控、与用户实际使用面一致、不引入代号模型；改动面小（PATCH 一个 provider 的 `models`）。
- 代价：Magpie 侧新增模型不会自动出现（需要人工挑选）；仍要人工决定 alias。
- 建议折中：**白名单（12~20 条）+ 自动填 4 字段 + alias 显式维护表**；并把 `prefix` 打开以消除撞名（实测 `prefix` 支持：`config_v8.go:384` 共享字段；`applyModelPrefixes` `service_models.go:705-752` 在 `prefix` 非空时同时注册裸名与 `prefix/<alias>`，`routing.force-model-prefix: true` 时只保留带前缀名——当前 `routing.force-model-prefix: false`）。

### 5.4 另一条硬约束

Magpie 的 `/v1/models` 是**唯一可信的 id 源**，但 CPA 侧 `name` 必须精确等于其中之一；由于 provider 前缀 id（`modelscope/...`）在 Magpie 不可寻址（404），**同步器不能“按 owned_by + 模型名”拼 id**，只能整条使用列表里的 `id`。

---

## 6. 实测命令与原始证据

原始响应（已保存，未含任何密钥）：

- `recon/raw/magpie-models-raw.json` — 默认 UA，34 条，22,119 B，`sha256=2c049c3bebeb7c1396a390275e4a4058a5c3d552db58878a1c264b5b321f93e4`，抓取时刻 `2026-10-30 12:38:19 +08:00`（响应头 `Date: Fri, 30 Oct 2026 04:38:19 GMT`），HTTP 200
- `recon/raw/magpie-models-cursor-ua.json` — `cursor-agent-exec/3.23.12`，34 条，32,158 B（含 `api_types`/`capabilities`）
- `recon/raw/magpie-models-claudedesktop-ua.json` — `claude-desktop/1.2.3`，32 条，15,100 B（`anthropic/magpie-*`、`mythos-magpie-*` 特化目录）
- `recon/raw/cpa-magpie-provider.json` — CPA `api-keys.openai-compatibility[8]` 全块，`keys` 已置 `***REDACTED***`

复现命令（key 全程只在远端 shell 变量里，不落盘、不回显；打印时只取前 6 位）：

```bash
# 1) Magpie /v1/models（build-host 上执行，直连本机网关）
CK=/srv/magpie/config/magpie/caller-keys.json
ssh build-host "K=\$(python3 -c \"import json;print(json.load(open('$CK'))[1]['secret'])\"); \
  curl -sS -m 20 -D /tmp/mh.txt -H \"Authorization: Bearer \$K\" \
       http://198.51.100.10:3425/v1/models -o /tmp/models_key0.json; \
  cat /tmp/mh.txt; sha256sum /tmp/models_key0.json; stat -c%s /tmp/models_key0.json"

# 2) 鉴权 / 分页 / 形状探测
ssh build-host "K=…; B=http://198.51.100.10:3425
  curl -s -o /dev/null -w '%{http_code}\n' \$B/v1/models                     # 401（无 key）
  curl -s -o /dev/null -w '%{http_code}\n' -H \"x-api-key: \$K\" \$B/v1/models  # 200
  curl -s -o /dev/null -w '%{http_code}\n' -H 'Authorization: Bearer sk-bogus' \$B/v1/models  # 401
  for q in '?limit=2' '?offset=32' '?after=auto/kimi-k3' '?page=2'; do
    curl -s -H \"Authorization: Bearer \$K\" \"\$B/v1/models\$q\" | python3 -c 'import json,sys;d=json.load(sys.stdin);print(len(d[\"data\"]),d[\"has_more\"])'
  done                                                                        # 全部 34 False
  curl -s -H \"Authorization: Bearer \$K\" \"\$B/v1/models?format=text\" | wc -l   # 34
  curl -s -H \"Authorization: Bearer \$K\" \"\$B/v1/models/auto%2Fkimi-k3\" | head -c 200   # 200
  curl -s -H \"Authorization: Bearer \$K\" \"\$B/v1/models/modelscope%2Fdeepseek-v4-pro\" # 404 unknown model
  for ua in curl/8.5.0 cursor-agent-exec/3.23.12 claude-desktop/1.2.3 grok-shell/1.0; do
    curl -s -H \"Authorization: Bearer \$K\" -H \"User-Agent: \$ua\" \$B/v1/models -o /tmp/ua.json; \
    python3 -c 'import json;d=json.load(open(\"/tmp/ua.json\"));k=set();[k.update(m) for m in d[\"data\"]];print(len(d[\"data\"]),sorted(k))'
  done"

# 3) CPA 侧配置（cpa-host）
ssh cpa-host 'python3 -c "
import yaml,json
d=yaml.safe_load(open(\"/srv/cliproxyapi/config.yaml\"))
e=[x for x in d[\"api-keys\"][\"openai-compatibility\"] if x.get(\"name\")==\"Magpie\"][0]
print(json.dumps({k:(\"***\" if k==\"keys\" else v) for k,v in e.items()}, ensure_ascii=False, indent=1))"'

# 4) CPA 运行时 /v1/models（只读 GET，用 access.api-keys[0]）
ssh cpa-host 'python3 -c "
import yaml,json,urllib.request
d=yaml.safe_load(open(\"/srv/cliproxyapi/config.yaml\")); k=d[\"access\"][\"api-keys\"][0]
r=urllib.request.Request(\"http://127.0.0.1:8317/v1/models\", headers={\"Authorization\":\"Bearer \"+str(k)})
b=json.load(urllib.request.urlopen(r, timeout=15)); print(len(b[\"data\"]))"'
# → 31 条；owned_by==\"Magpie\" 的仅 4 条
```

抓取失败情况：无。所有请求均 200/401/404 按预期返回，无超时、无 TLS 问题（内网直连 + 公网映射 `http://192.0.2.10:7509/v1/models` 亦 200，22,119 B 与内网一致，证明 CPA 走公网映射拿到的内容相同）。

补充证据（CPA 日志，`/srv/cliproxyapi/logs/main.log`，已脱敏）：

```
[2026-10-30 12:29:08] [warn] openai-compatible-magpie executor: upstream served model "DeepSeek-Flash"
  for requested model "auto/dfmodel" (auth_index=<auth-index>)
[2026-10-30 12:24:16] [info] session-affinity: ... auth=openai-compatibility:<provider-id>:<auth-id> provider=mixed model=deepseek-flash
[2026-10-30 12:32:14] [warn] 504 | 1m0.08s | upstream execution failed: provider=openai-compatible-magpie
  model=auto/dfmodel auth=api_key=<redacted> err=<html>...
```

- 第 1 条说明 Magpie 会用自己的 `display_name` 返写响应里的 `model` 字段（`DeepSeek-Flash`），与请求的 `auto/dfmodel` 不一致 → CPA 侧可考虑 `force-mapping: true`（**推断**，未实测其效果）。
- 第 2 条说明同 alias 多 provider 时 CPA 走 “mixed” 选路（Magpie 仍可被选中，只是展示层被别家覆盖）。
- 第 3 条说明 `auto/dfmodel` 这条通道近期有过 504（上游 html 错误页），同步时对这类模型应有可用性标记。

---

## 7. 未验证 / 需后续确认

1. `force-mapping: true` 对 Magpie “响应 model 字段回写”的实际效果（只读限制，未改配置实测）。
2. 两个 caller key（`CLIPro…` / `Magpie`+`lan:true`）响应字节数相同（22,119），**推断**不存在按 key 的模型裁剪；未做 key 维度的逐字段 diff（未发现差异点）。
3. `pricing` 只在“本机/被 gateway key 共享的调用方”返回（源码 `told := local(r) || sharedWith(r)`，`gateway.go` 内），CPA 经公网映射访问时是否仍带 `pricing` 未单独实测（内网两个 key 都带）。
4. `auto/*` 路由组的确切成员工厂（哪些 provider 参与某个组）只能从 Magpie GUI/`providers.json`+`settings.json` 反推；`settings.json` 里 `modelSameAs` 为空，组是“同名自动发现”，成员需扫码级排查，未展开。
5. `magpie_label` 里 “routing group” 后缀受 `plainNames/plainOwnNames` 设置影响（当前未开），沿用默认命名即可。
