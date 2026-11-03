# model-meta-sync 0.1.1 独立验收报告

- 验收人：plugin-scout（独立验收方，非作者），立场：怀疑式对抗
- 报告版本：v1（草稿即结论版，2026-10-30 14:26）
- 验收对象：**生产 0.1.1**，`.so` sha256 `d0c125d7cc993024374f49f0212f5845a99cc9b1ce1da10de3c8dc9d45eb51bf`
- 纪律：生产仅只读 SSH；未改生产、未重启；写入仅限本文件与 `recon/raw/`

## 0. 结论

**需修复后才能视为「完全可上线」。**

生产当前状态**没有发现会写坏 config 或泄漏 provider key 的阻塞级缺陷**，0.1.1 的核心硬化（provider 级白名单、thinking 子路径、冲突检测）经对抗测试**有效**。

但存在 **1 个阻塞项（回滚链路失效）**：`rollback_prod.sh` 的默认路径在生产上**不会真的回滚**。

| 类别 | 阻塞 | 非阻塞 |
|---|---|---|
| 代码/运行行为 | 0 | 6 |
| 部署/回滚 | **1** | 2 |
| 未覆盖（不判 PASS） | — | 2 |

---

## 1. 验收基线固定（先解决「验的是哪一版」）

验收期间源码被作者多次改动，因此**先冻结、再审查**，所有 PASS/FAIL 只对下列 hash 有效。

| 产物 | sha256 | 说明 |
|---|---|---|
| 生产 `.so` `model-meta-sync-v0.1.1.so` | `d0c125d7…51bf` | 与 build-host 沙箱 `.so` **完全一致** |
| 生产 config.yaml | `41d9d872…693fe9` | 部署后**未被插件再写** |
| 审查用源码 sync.go | `cace6cdd…b1e5` | `recon/raw/src-frozen-20261030-134948/sync.go` |
| 审查用源码 main.go | `f81dbc47…fbf8` | 同上 |

**⚠ 待发布变更（不计入本次验收）**：工作区 `plugin/sync.go` 现为 `a5832950…b778`（14:12:28 修改，备份清理逻辑），`sync_test.go` 为 `c2d0aec3…79dd`。**生产未部署此变更**，本次结论不覆盖它，需重新走一遍等价验收。

另：13:35–13:37 期间作者在改源码（验收开工后），13:37 与 13:49 两次冻结的 sync.go 不同（`cace6cdd` 前是旧版 `pathAllowed` 2 参数）。说明**先冻结再验**是必要的，本报告结论仅对 `cace6cdd` 成立。

---

## 2. 代码级审查（第 1 项必查清单）

### 2.1 `pathAllowed` / `validateSemanticDiff` 可绕过性 —— **PASS**

0.1.1 已从「字段名白名单」升级为「**provider 级白名单 + 结构锚定**」（`sync.go:869-912`）：

```go
base := 0
if parts[0]=="api-keys" && parts[1]=="openai-compatibility" { base = 2 } else if parts[0]=="openai-compatibility" { base = 1 } else { return false }
providerIndex, err := strconv.Atoi(parts[base]); if err != nil || !allowedProviders[providerIndex] { return false }
if parts[base+1] != "models" { return false }
if _, err := strconv.Atoi(parts[base+2]); err != nil { return false }
field := parts[base+3]; if !allowed[field] { return false }
// thinking 仅允许 levels[.<j>]
```

对抗矩阵 **10/10 全部符合预期**（原始输出 `recon/raw/audit-adversarial-0.1.1.txt`）：

| 攻击路径 | 期望 | 实测 |
|---|---|---|
| `…compatibility.0.models.3.display-name` | 允许 | ✅ 允许 |
| `…0.models.3.thinking.levels.0` | 允许 | ✅ 允许 |
| `…0.models.3.name` | 拒绝 | ✅ 拒绝 |
| `…0.models.x.display-name`（非数字 model 索引） | 拒绝 | ✅ 拒绝 |
| `…compatibility.X.models.0.display-name`（非数字 provider 索引） | 拒绝 | ✅ 拒绝 |
| `…compatibility.1.models.0.display-name`（**未选中 provider**） | 拒绝 | ✅ 拒绝 |
| `…0.models`（**模型数量变化**） | 拒绝 | ✅ 拒绝 |
| `completely.unrelated.0.models.0.display-name`（错误祖先） | 拒绝 | ✅ 拒绝 |
| `…thinking.levels.x` | 拒绝 | ✅ 拒绝 |
| 旧布局 `openai-compatibility.0.models.0.display-name` | 允许（兼容） | ✅ 允许 |

→ **旧版可被「任意祖先 + 任意 provider」绕过的洞已封**；跨 provider 泄漏写在**校验层**会被拒（见 2.3 的例外）。

### 2.2 模型数量变化 / 类型变化 / 重复键

| 场景 | 结论 | 证据 |
|---|---|---|
| `models` 条目数增减 | **拒绝** | 序列长度不等时 `collectDiff` 报父路径 `…models`，`pathAllowed` 因 `len(parts)<5` 返回 false（矩阵已验证） |
| 允许字段的**类型变化**（`max-context-length: "abc"` → int） | 允许（设计如此） | 实测 `written=true` 且结果仍是合法 YAML；不损坏文件 |
| config 内**重复键** | **拒绝（fail-closed）** | 实测 `err=有,writen=false,shaChanged=false`；`yaml.Unmarshal` 在重新解析原始配置时报错 → 不写 |

### 2.3 YAML 锚点 / 别名保护 —— **部分 FAIL（非阻塞，报告误导）**

0.1.1 新增守卫（`sync.go:618-622`）：
```go
if prov.models.Anchor != "" || prov.models.Kind == yaml.AliasNode || hasAliasModels(providers, prov.index) {
    prep.Skipped = "models list uses yaml anchors/aliases shared across providers; skipped for safety"
    continue
}
```
`hasAliasModels`（`sync.go:916-926`）只检查**其它 provider 的 `models` 节点本身是否为 AliasNode**。

**实测两个漏网形态（`audit-adversarial-0.1.1.txt`）**：

1. **同 provider 内条目级锚点**：`models: [&m {name: alpha}, *m]`
   - 实测：`skipped="models list contains yaml aliases; skipped for safety"` **但 `written=true`、sha 已变、changed=1**
   - 根因：`sync.go:637-641` 在循环里遇到 AliasNode 才 `break`，**之前已 `applyUpstreamFields` 改过的条目留在 doc 里**，`changedTotal>0` 仍继续编码+校验+写盘
   - 后果：**「skipped for safety」不等于「没写」**。语义上改的是同一节点（provider 内），无跨 provider 危害，但**报告与行为自相矛盾**
2. **跨 provider 共享锚点且两家都被选中**：provider A `models:[&shared {...}]`，provider B `models:[*shared]`，`providers:[A,B]`
   - 实测：`written=true`，A `changed=1`，B 报 `skipped`，但 B 的模型元数据**通过共享节点被一并改写**
   - 根因：A 的 `models` 是普通 SequenceNode（`Anchor==""`、非 AliasNode），B 的 `models` 也是 SequenceNode（别名在**条目**上），`hasAliasModels` 不命中 → A 不跳过；`selectedIdx={0,1}` 使 B 的路径也在白名单内 → 校验放行
   - 危害面：**窄**（要求同时选中两家 + 共享锚点）。生产只选 `providers: [Magpie]`，不触发

**建议**：把「被任何 provider 以任何形式引用（含条目级 AliasNode）」都纳入跳过条件；或在 `applyUpstreamFields` 前预先扫描整个 `models` 内容树，发现任何 `AliasNode`/`Anchor!=""` 即整家跳过且**回滚已改节点**。

### 2.4 `disabled` 判定 —— **FAIL（非阻塞）**

`sync.go:608` 用字符串比较：
```go
if disabled := scalarValue(mapLookup(prov.node, "disabled")); disabled == "true" {
```
实测 `disabled: yes`（YAML 1.1 布尔写法）→ **不识别**，`skipped=""`、`written=true`、sha 已变。
即：**操作者以为是「已停用」的 provider，仍会被同步并写盘**。
生产 config 用 `disabled: true/false` 规范写法，不触发；属健壮性缺陷。

### 2.5 `writeConfigAtomic`：冲突检测 / 回滚 / staging 清理 —— **PASS（有 1 个明确风险）**

| 检查点 | 结论 | 依据 |
|---|---|---|
| 冲突检测（并发人工保存） | **PASS** | 写前重读并比对 `doc.hash`；实测冲突时返回 `config changed while syncing; retry`，且**人工修改被完整保留** |
| 备份先于写 | **PASS** | 备份写失败即 abort（`sync.go:992-995`） |
| staging 权限/清理 | **PASS** | `CreateTemp` + `defer os.Remove`，`Chmod` 继承原 mode |
| 目录 fsync | **PASS** | `sync.go:1025-1028` |
| 备份轮转 | **PASS（逻辑本身）** | `pruneBackups` 保留 `BackupKeep`（默认 10）；注意**该逻辑正在被改动**（见 §1 待发布变更） |
| **原地 O_TRUNC 写的崩溃风险** | **RISK（非阻塞）** | `writeFileInPlace` 用 `O_WRONLY\|O_TRUNC`（`sync.go:1034`）。**若进程在 truncate 与 write 之间被 kill/断电，config 会留下截断内容**。其「自动回填」只在函数*返回错误*时执行，**崩溃不走这条路**。设计文档已明确此权衡（为触发 inotify 热重载），且有 `.bak`，但需知悉：这是唯一「可能写坏 config」的路径 |

**TOCTOU 窗口**：`os.ReadFile` 比对 hash 与 `O_TRUNC` 之间有极短窗口，竞态写入仍可能被覆盖。窗口远小于人工保存节奏，可接受；若要彻底消除需 `flock`。建议列为改进项。

### 2.6 错误路径是否会写坏 config —— **PASS**

对抗测试 5 类上游异常**全部 `written=false` 且 sha 未变**（见 §3.a）。另：任一 provider 上游失败 → 整轮不写（`sync.go:676-682`，fail-closed）。编码失败/校验失败 → 直接返回错误，不落盘。

### 2.7 provider key 泄漏面 —— **PASS（有 1 处不一致）**

| 通道 | 结论 |
|---|---|
| `/status`、`/config` 的 `config` 字段 | ✅ `api_key` 打码为 `***`（`main.go:156-162`） |
| 上游错误消息 | ✅ URL 经 `redactURL` 脱敏（userinfo），`snippet()` 只截 body 前 200 字符 |
| **`/config` 的 `raw_config_yaml`** | ⚠ **返回插件配置块原文，若操作者在 `plugins.configs` 里显式写了 `api_key`，此处为明文** —— 与同响应对 `config` 字段的打码不一致 |
| config.yaml 写路径 | ✅ 插件只改 `models[i].<field>`，不触碰 `keys`/`api-key`（校验层亦禁止） |

风险等级：仅对已持有管理密钥者可见，**非阻塞**；建议对 `raw_config_yaml` 做同样的 key 打码。

### 2.8 其它代码级发现（非阻塞）

| # | 发现 | 位置 |
|---|---|---|
| N1 | `collectDiff` 深度上限 12 时**直接 return，不记录路径**（fail-open）：深度 >12 的变化对校验完全不可见 | `sync.go:928-930` |
| N2 | **多文档 YAML 静默丢弃**：`yaml.Unmarshal` 只取第一个文档，`encode()` 只写第一个 → 第二个文档内容丢失。实测 `---\nsecret: keep-me` 被删除 | `encode()` `sync.go:431-442` |
| N3 | `metadata().ConfigFields` 未声明 `overwrite_fields` / `backup_keep` / `upstream_models_path`，管理端表单无法渲染这 3 项，而生产正在用其中 2 项 | `main.go:60-69` vs `config.go:33,37,45` |
| N4 | `plugin.register` 与 `plugin.reconfigure` 共用 `handleRegister`，重配置时新建 state，旧后台 goroutine 的 `runSync` 可能仍在写（`syncMu` 是新实例的）；窗口极小 | `abi.go:151-153`, `:211-231` |
| N5 | `Shutdown` 只 `inFlight.Wait()`，**不含后台同步 goroutine**；且从不关闭 `activeBackground.stop`。生产 `interval_minutes: 0` 使该 goroutine 只跑一次，风险低 | `abi.go:140-147`, `main.go:187-224` |
| N6 | 上游错误 body 若回显 `Authorization`，会被 `snippet()` 带进报告并经 `/status` 暴露 | `sync.go:235`, `:275-285` |

---

## 3. 对抗测试结果（第 2 项必查清单）

全部用 `httptest` 直驱 `runSync`（真实读写临时 config 文件），源码为冻结版 `cace6cdd`。
原始输出：`recon/raw/audit-adversarial-0.1.1.txt`；测试代码：`recon/raw/audit_review_test.go`、`recon/raw/audit_fixture_test.go`。

| 编号 | 场景 | 结果 |
|---|---|---|
| a1 | 上游 401 | ✅ `written=false`，sha 不变 |
| a2 | 上游返回 HTML（非 JSON） | ✅ 同上 |
| a3 | 空 body | ✅ 同上 |
| a4 | JSON 顶层是数组而非 object | ✅ 同上 |
| a5 | 截断 JSON | ✅ 同上 |
| a6 | 上游**重复 id** | ✅ 取**首条**（`FIRST/100` 生效，`SECOND/999` 未写入） |
| a7 | 上游**缺 id / 空 id** | ✅ 全部忽略，未泄漏进 config |
| a8 | 超大响应 | ✅ `io.LimitReader(…, 16<<20)` 封顶，截断后解析失败 → 不写（未实测 16MiB+ 具体字节，见未覆盖 U2） |
| b1 | config 带注释 | ✅ 注释保留（yaml.Node 往返） |
| b2 | config 带**锚点/别名（条目级）** | ⚠ **FAIL**：报 skipped 但**仍写盘**（§2.3） |
| b3 | 跨 provider 共享锚点且两家均选中 | ⚠ **FAIL**：写盘且泄漏到被跳过的一方（§2.3） |
| b4 | config 带**重复键** | ✅ fail-closed，不写 |
| b5 | config 为**多文档** | ⚠ **FAIL**：第二文档被静默丢弃（N2） |
| b6 | 引号 / 单引号风格 | ✅ 语义不变（仅文本风格可能变化，见 §4 生产 diff 复核） |
| c1 | **非目标 provider 是否 0 改动** | ✅ **字节级完全一致**（`Other` 块前后逐字节相同） |
| d1 | `missing` + `overwrite_fields`：display-name 覆盖 | ✅ 覆盖为 `UP-NAME` |
| d2 | `missing` + `overwrite_fields`：**thinking 不覆盖** | ✅ `levels: ["low","medium","high"]` 原样保留 |
| e1 | 写入前并发修改 config | ✅ 拒绝（`config changed while syncing; retry`）且人工改动保留 |
| f1 | `max-context-length: "abc"` | ✅ 安全修正为 int，结果合法 YAML |
| f2 | `max-context-length: {a: b}`（map） | ✅ 安全修正，合法 YAML |
| f3 | `thinking: "off"`（标量） | ✅ 安全处理，合法 YAML |
| f4 | `thinking: [a,b]`（列表） | ✅ 安全处理，合法 YAML |
| f5 | `input-modalities: "text"`（标量） | ✅ 安全处理，合法 YAML |

所有异常类型用例：`written=true`、`stillValidYAML=true` —— **不损坏配置**。

---

## 4. 生产只读核验（第 3 项必查清单）

| 检查点 | 期望 | 实测 | 结论 |
|---|---|---|---|
| 生产 `.so` 版本/哈希 | `model-meta-sync-v0.1.1.so` `d0c125d7…51bf` | 文件名 `model-meta-sync-v0.1.1.so`，sha256 `d0c125d7…51bf`（13:39） | ✅ |
| 0.1.0 已清理 | 不存在 | 目录内无 `model-meta-sync-v0.1.0.so` | ✅ |
| `.so` 与 build-host 一致 | 一致 | build-host `/tmp/mms-stage/.sandbox/plugins/linux/amd64/` 同名同 sha | ✅ |
| config sha256 | `41d9d872…693fe9` | `41d9d872…693fe9` | ✅ |
| 插件写前备份 | 1 个 | `config.yaml.bak-modelmetasync-20261030-132726`（27161B） | ✅ |
| 部署备份 | 2 个 | `…-deploy-20261030-132722`（26502B，**插件块之前**）、`…-deploy-20261030-133930`（27161B，**插件块之后**） | ✅ |
| config 语义 diff | 仅 3 条 max-context-length | `diff …-132726 config.yaml` → **恰好 3 处** `max-context-length: 1000000 → 1048576`，无其它差异 | ✅ |
| 进程/监听 | 存活，8317 | pid 2571934，`LISTEN *:8317` | ✅ |
| 服务托管 | — | systemd **user** unit `/root/.config/systemd/user/cliproxyapi.service`，`active`+`enabled`（system 作用域找不到，属正常） | ✅ |
| 插件 0.1.1 **已加载**日志 | 有 `version=0.1.1` | ❌ **`main.log` 中 `0.1.1` 出现 0 次**；仅有 13:39:30 `plugin unloaded … version=0.1.0` | **未覆盖 U1** |
| `/v1/models` 31 条 | 31 | ❌ 无客户端 key（`/v1/models` → 401；`api-keys.access` 为 null），未独立取得 | **未覆盖 U3** |
| 9 providers / 12 条 Magpie 模型 | 9 / 12 | 未逐项核（`max-context-length` 计数 42 条 ≠ 模型条数，需按块精确解析） | **未覆盖 U3** |

**关于 U1 的判断**：不能凭日志确认 0.1.1 生效。间接证据链为——① 13:39:30 记录**卸载 0.1.0**；② 同刻重启；③ 盘上 `.so` 为 0.1.1 且 sha 与 build-host 构建一致；④ 0.1.1 `sync_on_start: true` 应在 +3s 跑一次，而 config 未被再写（`config_changed:false` 与「无变化→不写」一致）；⑤ 补偿：`main.go:16` `pluginVersion="0.1.1"`，若真加载了 0.1.1，`/status` 会返回 `0.1.1`。**建议**：由持管理密钥者 `curl -H "X-Management-Key: …" http://127.0.0.1:8317/v0/management/model-meta-sync/status` 取 `version` 字段即可定论（我未尝试，避免触发 5 次失败封禁 30 分钟）。

---

## 5. 回滚 / 部署脚本审查（只读，未执行）

### 5.1 【阻塞】`rollback_prod.sh` 在生产上不会真的回滚

`runbook/rollback_prod.sh`：

```bash
SO=/srv/cliproxyapi/plugins/linux/amd64/model-meta-sync-v0.1.0.so   # ← 硬编码 0.1.0
backup=$(ls -1t "$CONFIG".bak-modelmetasync-deploy-* | head -1)       # ← 取最新
cp -a "$CONFIG" "$CONFIG.bak-modelmetasync-rollback-$(date …)"
cp -a "$backup" "$CONFIG"
rm -f "$SO"                                                          # ← 删不到东西
XDG_RUNTIME_DIR=/run/user/0 systemctl --user restart cliproxyapi.service
```

生产实测两个 deploy 备份的**语义差别**：

| 备份 | 大小 | 内容 | 恢复后效果 |
|---|---|---|---|
| `…-deploy-20261030-132722` | 26502 B | **插件块之前**（与 10-30 07:56 的 config 同尺寸） | 正确：移除 `plugins.configs.model-meta-sync` |
| `…-deploy-20261030-133930` | 27161 B | **插件块之后**（与 13:27 插件写前备份同尺寸） | ❌ 仍保留 `plugins.configs.model-meta-sync: enabled: true` |

默认分支 `ls -1t | head -1` 取**最新** = `…-133930`。因此默认回滚执行后：
1. `.so` 删除命令指向 **v0.1.0**（已不存在）→ `rm -f` 静默成功但**什么都没删**，`model-meta-sync-v0.1.1.so` 仍在；
2. 恢复的 config **仍然启用** `model-meta-sync`；
3. 重启后插件**照常加载**（host 按 `plugins.configs` 找 `.so`，文件在、开关在）。

→ **「回滚」后插件仍在生产运行，且它会继续 `sync_on_start` 写 config。** 这是本次唯一阻塞项。
另外若显式传入 `…-132722`，脚本行为才是正确的，但脚本**不会提示该风险**。

**最小修复**：`SO` 改为 glob（`rm -f …/model-meta-sync-v*.so`），并在恢复后**校验** `plugins.configs.model-meta-sync` 不存在、`.so` 已无；备份选择改为**显式指定**或筛选「不含插件块」的那份（可用 `grep -L model-meta-sync <bak>`）。

### 5.2 非阻塞

| # | 发现 |
|---|---|
| N7 | `deploy_prod.py` 靠匹配字面行 `"    configs:"` 插块，缩进一变就 `SystemExit`；且幂等检查只判 `model-meta-sync` 键**是否存在**，**不更新已存在块**——日后若块内字段升级（如新增 `overwrite_fields` 值），重部署不会生效 |
| N8 | `deploy_prod.py` 用 `access.api-keys[0]` 做探活；生产 `api-keys.access` 为 null（v8 布局差异），该探活会退化为无鉴权请求 → `gateway_up` 可能误判（本次部署显然通过了，但判据不可靠） |

---

## 6. 阻塞 / 非阻塞清单

### 阻塞（1）
- **B1 回滚链路失效** —— `runbook/rollback_prod.sh`：`SO` 硬编码 `v0.1.0`；默认备份为「含插件块」的最新 deploy 备份。默认执行 = 插件仍在生产运行并继续写 config。

### 非阻塞（7）
- N1 `collectDiff` 深度>12 fail-open（`sync.go:928`）
- N2 多文档 YAML 第二文档静默丢失（`encode()`）
- N3 `metadata().ConfigFields` 漏声明 `overwrite_fields`/`backup_keep`/`upstream_models_path`
- N4 `reconfigure` 与 `register` 共用路径，旧后台 sync 可能与新实例并发写
- N5 `Shutdown` 不等待后台同步 goroutine
- N6 上游错误 body 回显 Authorization 时会进 `/status`
- N7/N8 §5.2 部署脚本幂等与探活判据
- N9 `disabled: yes` 不识别（§2.4）
- N10 条目级锚点：报「skipped for safety」但仍写盘；跨 provider 且两家均选中时泄漏写（§2.3）
- N11 `/config` 的 `raw_config_yaml` 未对 `api_key` 打码（§2.7）
- N12 原地 `O_TRUNC` 写在崩溃窗口内可能留下截断 config（§2.5）

### 未覆盖（2）
- **U1** 「0.1.1 已加载」的**正面日志证据**缺失（`main.log` 中 `0.1.1` 出现 0 次）。需管理密钥查 `/status.version` 定论。
- **U3** `/v1/models` 31 条、9 providers、12 条 Magpie 模型**未独立核验**（无客户端 key / 未逐块解析）。
- **U2** 16MiB+ 超大响应未做真实大文件实测（仅代码审查确认有 `LimitReader` 上限）。
- **U4** 未在 build-host amd64 沙箱跑端到端植入 .so（时间所限），当前结论为「源码级 + httptest 直驱 + 生产只读」三重交叉，**缺真实宿主加载**的运行时验证。

---

## 7. 复现命令

```bash
# 0) 冻结与对照（已执行，见 §1）
sha256sum recon/raw/src-frozen-20261030-134948/*.go        # cace6cdd…(sync.go)
sha256sum plugin/sync.go                                    # a5832950… ← 待发布变更，勿当生产

# 1) 代码级 + 对抗测试（arm64 本地，httptest 直驱 runSync）
export PATH=$PWD/.toolchain/go/bin:$PATH
cp -a recon/raw/src-frozen-20261030-134948 /tmp/audit && cd /tmp/audit
rm -f design.md deploy.md deploy_prod.py rollback_prod.sh SHA256SUMS FROZEN-AT.txt
cp <repo>/recon/raw/audit_*_test.go .
CGO_ENABLED=1 go test -run TestAudit -v ./...

# 2) 生产只读核验
ssh cpa-host 'sha256sum /srv/cliproxyapi/config.yaml; \
  sha256sum /srv/cliproxyapi/plugins/linux/amd64/model-meta-sync-v0.1.1.so; \
  ls -l /srv/cliproxyapi/config.yaml.bak-modelmetasync-*'
ssh cpa-host 'diff /srv/cliproxyapi/config.yaml.bak-modelmetasync-20261030-132726 \
  /srv/cliproxyapi/config.yaml'                     # 期望恰好 3 处 max-context-length
ssh cpa-host 'grep -c "0\.1\.1" /srv/cliproxyapi/logs/main.log'   # 实测 0 ← U1
ssh cpa-host 'XDG_RUNTIME_DIR=/run/user/0 systemctl --user is-active cliproxyapi.service'

# 3) 回滚脚本缺陷复现（只读，不执行脚本）
ssh cpa-host 'ls -1t /srv/cliproxyapi/config.yaml.bak-modelmetasync-deploy-* | head -1'
#   → …-133930（27161B，含插件块）；而 …-132722（26502B）才是回滚应选的那份
ssh cpa-host 'ls /srv/cliproxyapi/plugins/linux/amd64/model-meta-sync-v0.1.0.so'
#   → No such file：脚本的 rm -f 目标是空的

# 4) 定论 U1（需管理密钥，未由我执行）
ssh cpa-host 'curl -s -H "X-Management-Key: <key>" \
  http://127.0.0.1:8317/v0/management/model-meta-sync/status'   # 看 version 字段
```

---

## 8. 判级说明

- 判定「**需修复**」的唯一理由是 B1：回滚是上线前的最后一道闸，当前它在生产上无效。修 B1 后本报告可升为「可上线」。
- 0.1.1 相对 0.1.0 的三项硬化中，**provider 级白名单**与**冲突检测**经对抗实测有效；**锚点保护**只覆盖了「models 列表本身被别名」的一半场景（N10），但**校验层能兜住跨 provider 泄漏**（除非两家都被选中）。
- **待发布变更（`a5832950` 备份清理逻辑）不得沿用本结论**，需重跑 §7 第 1 步并对齐新 hash。

---

# v2 增量复核（0.1.2，2026-10-30 15:0x）

- 复核范围：B1 回滚闭环、锚点第三种绕过、`grep` 判据稳健性，以及 9 项改动逐条验证 + v1 用例回归
- 冻结基线：`recon/raw/src-frozen-20261030-150140/`
  - `sync.go` `959bcf0c…bc8d`｜`main.go` `ca989db2…8993d`｜`rollback_prod.sh` `c6c17adb…7fe3`｜`deploy_prod.py` `12aac35d…ed0b`
- 生产：`.so` v0.1.2 `5d0a23f6…d9bd`｜config `aea6afa8…3813`
- 原始输出：`recon/raw/audit-v2-adversarial-0.1.2.txt`、`recon/raw/audit_v2_test.go`

## v2-0 结论

| 项 | 判定 |
|---|---|
| 第 2~9 项代码修复（锚点前置、disabled、多文档、深度、ConfigFields、打码、deploy、备份清理） | **PASS（8/8）** |
| v1 用例回归（上游异常/非目标 provider/overwrite/TOCTOU/异常类型） | **PASS（无回归）** |
| 生产 0.1.2 状态 | **PASS** |
| **B1 回滚闭环** | **部分闭环 —— 功能正确，但成功路径退出码错误 + 两处可靠性缺陷（仍判阻塞）** |
| **锚点第三种绕过** | **仍存在**（条件窄，生产不触发） |

**总判定：仍为「需修复」，但阻塞项已从「回滚无效」降级为「回滚有效但退出码/判据/顺序不可靠」。**

## v2-1 生产状态核验（PASS）

| 检查点 | 实测 | 结论 |
|---|---|---|
| `.so` 版本/哈希 | `model-meta-sync-v0.1.2.so` `5d0a23f6…d9bd` | ✅ 与告称一致 |
| 0.1.1 已清理 | journal 明示 `old plugin file removed … version=0.1.1` | ✅ |
| config sha256 | `aea6afa8…3813` | ✅ 与告称一致 |
| **插件已加载（v1 的 U1）** | journal 15:00:15 同时有 `plugin loaded … version=0.1.2` 与 `plugin registered … version=0.1.2` | ✅ **U1 关闭**（我 v1 查的是 `main.log`，该类 `host.go:362/385` 行只进 journal，不进文件日志——这是我 v1 的取样面错误，非插件缺陷） |
| providers | **9** | ✅ |
| Magpie 模型 | **12** | ✅ |
| Magpie `max-context-length` 取值集合 | `{1000000, 1048576}` | ✅ 与「3 条被修正、其余本就一致」相容（0.1.2 启动同步 0 变更即证 config 已与上游对齐） |
| `/v1/models` 31 条 | 仍未取得（`/v1/models` 无 key 返回 401；`access.api-keys` 在 v8 布局下取不到） | **U3 仍未覆盖** |

## v2-2 B1 回滚脚本：实测四组（build-host，`CPA_SKIP_RESTART=1`）

脚本 `c6c17adb…7fe3`；测试目录 `/tmp/rb-audit*`。

| 场景 | 期望 | 实测 | 判定 |
|---|---|---|---|
| **T-A 正常回滚**（存在 pre-install 备份 + 更新的含块备份） | 选旧的不含块那份、删除 .so、exit 0 | **选中 `…131000` 而非更新的 `…134000`** ✅；config 已还原 ✅；`.so` 剩 0 ✅；**但 `EXIT=2` 且未打印成功行** | **FAIL（退出码）** |
| T-B 无 pre-install 备份 | exit 1 + 提示显式路径 | `EXIT=1`，提示清晰，未做任何改动 | ✅ PASS |
| T-C 显式指定含块备份 | 拒绝、不重启 | `EXIT=1`，打印 `config still references model-meta-sync after restore; aborting before restart` | ⚠ 拒绝正确，但**已先删 .so**（见下） |
| T-D 判据：pre-install 备份里只有一句注释提到插件 | 应识别为「无插件块」并正常回滚 | 被判为「含块」→ 跳过 → **拒绝回滚**（`.so` 仍在、config 仍启用） | **FAIL（判据）** |

### 【阻塞 B1-a】成功路径 exit 2 —— 一行 bug

`rollback_prod.sh:55` 与 `:65`：
```bash
remaining=$(ls -1 "${PLUGIN_GLOB}"*.so 2>/dev/null | wc -l)
```
`set -euo pipefail`（`:8`）下，**无匹配时 `ls` 退出 2**，`pipefail` 使整条管道返回 2，赋值语句失败 → `set -e` 直接终止。而「无匹配」正是回滚成功的状态。

本地最小复现（同一 shell 语义）：
```bash
bash -c 'set -euo pipefail; d=/tmp/x; mkdir -p $d; touch $d/foo-v1.so
  r=$(ls -1 "$d"/foo*.so 2>/dev/null | wc -l); echo "MATCH: $r"
  rm -f $d/foo*.so
  r2=$(ls -1 "$d"/foo*.so 2>/dev/null | wc -l); echo "NO-MATCH: $r2"'
# 实测输出：MATCH: 1  然后 EXIT=2（"NO-MATCH" 从未打印）
```
build-host 实测同结论：T-A 功能全部正确但 `EXIT=2`。

**危害**：回滚是事故动作。自动化/`&&` 链会把成功判为失败，可能导致重复回滚或人工误操作。
**修复**（任选）：`remaining=$(find "$PLUGIN_DIR" -maxdepth 1 -name 'model-meta-sync-v*.so' | wc -l)`（find 无匹配返回 0）；或 `|| true`；或 `set +e` 包住该行。

### 【阻塞 B1-b】销毁动作早于校验（顺序错误）

`:42` `rm -f "${PLUGIN_GLOB}"*.so` 在 `:44` 的「config 仍引用插件则 abort」**之前**执行。
T-C 实测：显式传入含块备份时，脚本**先把 .so 删了**，再发现 config 仍启用插件并 abort → 系统停在 **config 说启用、二进制已消失** 的不一致状态（下次重启会报加载失败）。
**修复**：把 `rm -f` 移到 `:47` 之后（校验通过再删）。

### 【阻塞 B1-c】`grep -q 'model-meta-sync'` 作为判据不够稳（回答你的问题）

**不够稳。** 它是纯子串匹配，有两个方向的失真：

- **假阳性（已实测，T-D）**：pre-install 备份里只要有注释/文档字符串提到 `model-meta-sync`（例如 `# NOTE: model-meta-sync disabled for now`），该备份就被判为「含块」→ 被跳过。若所有候选都如此 → 脚本以「no pre-install backup found」拒绝回滚（安全但**阻断**）。
  更糟：`:44` 的后置断言用同一判据，**假阳性会在删除 .so 之后触发 abort**，与 B1-b 叠加成「config 启用 + 二进制消失」。
- **假阴性**：对「插件块是否存在」这一具体判断基本不可能（块键本身就是 `model-meta-sync:` 字面量）。所以**后置断言的方向是可靠的**，问题只在假阳性。

**建议改用结构化判据**（与 grep 等价但精确）：
```bash
has_block() { python3 - "$1" <<'PY'
import sys, yaml
cfg = yaml.safe_load(open(sys.argv[1])) or {}
sys.exit(0 if ((cfg.get("plugins") or {}).get("configs") or {}).get("model-meta-sync") else 1)
PY
}
```
（生产 host 已有 python3+yaml，deploy_prod.py 也在用。）

### 【非阻塞 B1-d】整文件回滚会连带丢失无关改动（新发现）

`pick_backup` 现在只会选到 **唯一** 的 pre-install 备份 `…-deploy-20261030-132722`，而它产生于 **13:27**。实测该备份 `access_keys=18`，当前 config `access_keys=19`——14:5x 有人在面板加的**第 19 个 access key 会在回滚时被一并抹掉**。

这不是脚本 bug（逻辑正确），而是「整文件恢复」这一策略的固有代价，但在事故场景下极易被忽略。
**建议**：回滚改为**只摘除插件块**（解析 YAML → 删除 `plugins.configs.model-meta-sync` → 回写），其余配置原样保留；或至少回滚后打印「已回退到 <ts>，该时间点之后的配置变更会丢失，差异如下」并给出 diff。

## v2-3 锚点/别名：第三种绕过（回答你的问题）

**仍存在一种。** 逐形态实测（0.1.2）：

| 形态 | 实测 | 判定 |
|---|---|---|
| 同 provider 内条目级锚点 `models:[&m {…}, *m]` | `skipped="models list uses yaml anchors/aliases; skipped for safety"`，**sha 未变、未写盘** | ✅ v1 的「报 skipped 但仍写」已修 |
| `models` 节点本身被别名 / 带 Anchor | 整家跳过 | ✅ |
| **provider 节点本身被锚定**（`- &p {…}` + `- *p`） | 校验层拒绝：`refusing to write: unexpected config changes: api-keys.openai-compatibility.1.models.0.display-name`，`written=false` | ✅ fail-closed |
| `thinking` 值为别名（`thinking: *lv`，锚点在顶层） | `written=false`，`&lv` 定义保留，无损坏 | ✅ |
| **跨 provider 条目级共享锚点 + 两家都被选中** | **`written=true`**；`Magpie2` 报 `skipped="…skipped for safety"`，但其 `models: - *shared` 指向的节点已被写入 `display-name: UP-alpha` → **被跳过的一方实际被改写** | **❌ 绕过仍在** |

**根因**：`hasAliasModels`（`sync.go:966-975`）只判断「其它 provider 的 `models` 节点本身是否为 AliasNode」；而此形态里对方的 `models` 是普通 SequenceNode，别名在**条目**上。`modelsListHasAlias` 只检查**自己**的条目。于是 A 不被跳过，A 的写入经共享节点传导到 B；又因两家都在 `selectedIdx` 内，`pathAllowed` 对 B 的路径也放行。

**触发条件（两个必须同时满足）**：① 两家 provider 的 models 条目共享锚点；② `providers:` 同时选中两家。
**生产现状**：`providers: [Magpie]` → **不触发**。故判非阻塞。
**修复建议**：把「被任何 provider 以任何形式引用」都算进去——遍历**所有** provider 的 `models.Content`，若任一条目 `Kind==AliasNode` 或 `Anchor!=""`，则**该节点所属 provider 与引用它的 provider 一并跳过**（当前只跳过后者的自身处理，未阻止前者写入共享节点）。

## v2-4 其余 7 项逐条验证

| # | 改动 | 实测 | 判定 |
|---|---|---|---|
| 3 | `isTruthy()` 处理 `disabled` | `yes`/`on`/`True`/`TRUE`/`true` 五种写法**全部** `skipped="provider disabled"`、sha 不变 | ✅ |
| 4 | `decodeSingleDocument` 拒绝多文档 | 报 `config file contains 2 YAML documents; refusing to rewrite multi-document configs`，文件未被改动 | ✅ |
| 5 | `collectDiff` 深度 fail-closed | 深度 15 的变更被记为 `"k.k.…k"` 路径 → 校验会拒绝 | ✅ |
| 6 | `metadata.ConfigFields` 补齐 | 已声明 `overwrite_fields`(69)、`backup_keep`(70)、`upstream_models_path`(71) | ✅ |
| 7 | `raw_config_yaml` 打码 | 改为 `redactConfigYAML(p.configYAML)`（`main.go:157,160-172`），失败时返回 `<redaction failed>` | ✅ |
| 8 | `deploy_prod.py` | `upsert_block`（插入或整块替换，`:58`）、探活改 `-w %{http_code}`（`:136`）、client key 兼容 `access.api-keys` 与 legacy `api-keys.access`（`:87-95`） | ✅（**只读审查，未执行**） |
| 9 | 备份清理不 prune deploy 备份 | 代码审查 + 单测声明；生产现有 4 个备份均在（含 2 个 deploy） | ✅（生产佐证） |

## v2-5 回归（v1 用例在 0.1.2 上重跑）

全部 **PASS，无回归**：上游 401/非 JSON/空 body/数组/截断 → 不写盘；重复 id 取首条；缺 id 忽略；非目标 provider **字节级零改动**；`overwrite_fields` 覆盖 display-name 但**保留 thinking**；TOCTOU 冲突检测 + 人工改动保留；6 类异常类型均产出合法 YAML。

## v2-6 未覆盖

- **U3** `/v1/models` 31 条仍未独立取得（无客户端 key）。9 providers / 12 Magpie 模型已用 config 直接核到。
- **U4** 仍未在 build-host 真实植入 .so 跑宿主级端到端（沙箱 CPA 进程已停）；本轮为「冻结源码 + httptest 直驱 + build-host 脚本实跑 + 生产只读」四重交叉。
- **U5** `deploy_prod.py` 的 `upsert_block` 未执行验证（仅代码审查；生产已部署，重跑会改动生产）。

## v2-7 阻塞/非阻塞（v2）

### 阻塞（3 项，同一脚本，均为一行级修复）
- **B1-a** 成功路径 `exit 2`（`ls|wc` + `pipefail`），`:55`/`:65`
- **B1-b** `rm -f .so` 早于 config 校验，`:42` vs `:44`
- **B1-c** `grep -q` 假阳性导致拒绝回滚（且在删 .so 后误报），`:24`/`:44`

### 非阻塞（3 项）
- **B1-d** 整文件回滚连带丢失 pre-install 之后的无关改动（实测将丢第 19 个 access key）
- **N13** 跨 provider 条目级共享锚点 + 两家均选中 → 被跳过方仍被改写（生产不触发）
- **N14** `redactConfigYAML` 失败时返回 `<redaction failed>` 占位（安全，但会掩盖原始配置，需知悉）

### 修复后判定
B1-a/b/c 三处修完（合计 <10 行），本报告可升为 **可上线**；B1-d 与 N13 建议同批处理，但不构成阻塞。

## v2-8 复现命令（增量）

```bash
# 冻结基线
cat recon/raw/src-frozen-20261030-150140/SHA256SUMS

# 代码级 v2 用例（arm64 本地）
export PATH=$PWD/.toolchain/go/bin:$PATH
cp -a recon/raw/src-frozen-20261030-150140 /tmp/a3 && cd /tmp/a3
rm -f SHA256SUMS FROZEN-AT.txt design.md deploy_prod.py rollback_prod.sh
cp <repo>/recon/raw/audit_v2_test.go <repo>/recon/raw/audit_*_test.go .
CGO_ENABLED=1 go test -run 'TestV2_|TestAudit_Upstream|TestAudit_NonTarget|TestAudit_Overwrite|TestAudit_TOCTOU|TestAudit_Anomaly' -v ./...

# B1-a 最小复现（不碰生产）
bash -c 'set -euo pipefail; d=/tmp/x; rm -rf $d; mkdir -p $d; touch $d/foo-v1.so
  r=$(ls -1 "$d"/foo*.so 2>/dev/null | wc -l); echo "MATCH: $r"; rm -f $d/foo*.so
  r2=$(ls -1 "$d"/foo*.so 2>/dev/null | wc -l); echo "NO-MATCH: $r2"'; echo "EXIT=$?"   # 期望打印两行并 EXIT=0，实测 EXIT=2

# B1-a/b/c 在 build-host 复跑（SKIP_RESTART，不重启任何服务）
scp recon/raw/src-frozen-20261030-150140/rollback_prod.sh build-host:/tmp/rollback_0.1.2.sh
ssh build-host 'D=/tmp/rb; rm -rf $D; mkdir -p $D/plugins/linux/amd64; cd $D
  printf "api-keys:\n  openai-compatibility: []\n" > config.yaml.bak-modelmetasync-deploy-20261030-131000
  printf "plugins:\n    configs:\n        model-meta-sync:\n            enabled: true\n" > config.yaml.bak-modelmetasync-deploy-20261030-134000
  cp config.yaml.bak-modelmetasync-deploy-20261030-134000 config.yaml
  touch plugins/linux/amd64/model-meta-sync-v0.1.2.so
  CPA_CONFIG=$D/config.yaml CPA_PLUGIN_DIR=$D/plugins/linux/amd64 CPA_SKIP_RESTART=1 bash /tmp/rollback_0.1.2.sh; echo "EXIT=$?"'

# 生产只读
ssh cpa-host 'sha256sum /srv/cliproxyapi/config.yaml /srv/cliproxyapi/plugins/linux/amd64/model-meta-sync-v0.1.2.so'
ssh cpa-host 'journalctl --user -u cliproxyapi --since "2026-10-30 14:55" --no-pager | grep -i "plugin loaded\|plugin registered"'
ssh cpa-host 'python3 -c "
import yaml;c=yaml.safe_load(open(\"/srv/cliproxyapi/config.yaml\"))
oc=c[\"api-keys\"][\"openai-compatibility\"];print(\"providers\",len(oc))
print(\"magpie\",[len(p[\"models\"]) for p in oc if p[\"name\"]==\"Magpie\"])"'
```

---

# v3 聚焦复核（0.1.3）—— 终审

- 范围：v2 的 3 个阻塞项 + B1-d + 锚点第三种绕过，逐条验；附全量回归与生产只读
- 冻结基线：`recon/raw/src-frozen-20261030-151337/`
  - `sync.go` `55bccb37…2a55`｜`main.go` `46de3673…84b1`｜`rollback_prod.sh` `9fb9dd4b…59d9`
- 生产：`.so` v0.1.3 `1fda55de…9ff6`｜config `aea6afa8…3813`（未变）
- 原始输出：`recon/raw/audit-v3-regression-0.1.3.txt`、`recon/raw/audit_v3_test.go`

## 终审结论：**可上线**

v2 的 5 项改动**全部实测通过**；全量回归 **23/23 PASS、0 FAIL**；生产 0.1.3 状态与告称一致。
残余 1 项 P1（`--restore` 分支的边角 fail-open，默认路径不受影响）建议同批打一行补丁，**不阻塞上线**。

## v3-1 改动逐条验证

| # | 改动 | 实测 | 判定 |
|---|---|---|---|
| 1 | **B1-a 退出码**：`find … \| wc -l \| tr -d ' '` | T-A 成功路径 **`EXIT=0`**，且打印 `rollback simulated` | ✅ 闭环 |
| 2 | **B1-b 顺序**：先校验 config 再删二进制 | T-E 用含块备份 `--restore` → `EXIT=1`、**`.so` 仍为 1、config 未被改动**（无任何销毁动作） | ✅ 闭环 |
| 3 | **B1-c 判据**：`has_block()` 改 python3+yaml | 见 v3-2 三组 | ✅ 闭环 |
| 4 | **B1-d 外科式摘除** | T-A：块被摘除，`keeper`/`antigravity-priority` 保留，**`key-added-later` 保留**，`.so=0` | ✅ 闭环 |
| 5 | **锚点全局判定** `yamlAliasesPresent` | 见 v3-3 四组 | ✅ 闭环 |

## v3-2 回滚脚本五组实跑（build-host，全部 `CPA_SKIP_RESTART=1`，未重启任何服务）

| 场景 | 期望 | 实测 | 判定 |
|---|---|---|---|
| **T-A** 默认外科式回滚（config 里 `model-meta-sync` 夹在 `keeper` 与 `antigravity-priority` 之间，且 `access.api-keys` 有后续新增项） | 只摘块、其余原样、exit 0 | `removed plugins.configs.model-meta-sync`；`keeper`/`antigravity-priority` 均在；**`key-added-later` 在**；`.so=0`；**`EXIT=0`** | ✅ |
| **T-D** pre-install 备份里含注释 `# NOTE: model-meta-sync disabled for now` | 不得假阳性 | 外科模式下不依赖备份；`EXIT=0`，块正确摘除 | ✅ **v2 的假阳性已消失** |
| **T-E** `--restore <含块备份>` | 拒绝、不销毁 | `refusing: backup … already contains the model-meta-sync block`；`EXIT=1`；`.so` 仍 1；config 原样 | ✅ |
| **T-F** 外科模式 + **python3 不可用** | 应 fail-closed | `failed to strip plugin block`；`EXIT=1`；块仍在、`.so` 仍在（**未销毁**） | ✅ |
| **T-G** `--restore` + **python3 不可用** | 应 fail-closed | 见 v3-4 R1 | ⚠ 见 R1 |

## v3-3 锚点全局判定四组（Go，直驱 `runSync`）

| 用例 | 实测 | 判定 |
|---|---|---|
| **V3-1 跨 provider 条目级共享锚点 + 两家都选中**（v2 的第三种绕过） | **`written=false`、文件字节完全不变**；两家均报 `provider model lists use yaml anchors/aliases; skipped for safety` | ✅ **v2 绕过已闭环** |
| V3-2 条目别名指向顶层锚点（单 provider） | 跳过、文件不变 | ✅ |
| V3-3 锚点出现在**未被选中**的 provider 上 | 整批跳过、文件不变 | ✅（全局保守策略，见 R3） |
| V3-4 干净配置（无任何锚点） | **仍正常同步并写盘** | ✅ 未过度拦截 |

## v3-4 残余项（非阻塞）

### R1（P1，建议同批修，1 行）`--restore` + python3 缺失 → 静默错误成功
`rollback_prod.sh:64-76` 与 `:94-105` 只判断 `status -eq 0`（有块）与 `status -eq 2`（不可解析）；
python3 不存在时 `has_block` 返回 **127**，两个分支都不命中 → **落到「无块」的处理**。
T-G 实测：恢复含块备份未被拦下、随后删掉 `.so`，最终 **`EXIT=0` + `rollback simulated`**，而 config 仍有 `model-meta-sync`（`grep -c`=1）、`.so`=0 —— 即「启用但无二进制」且**报成功**。
**最小修法**：两处 `case`/`if` 各加一个兜底分支
```bash
if [[ "$status" -ne 0 && "$status" -ne 1 ]]; then
  echo "refusing: cannot evaluate plugin block state (has_block=$status)" >&2; exit 1
fi
```
**影响面**：仅 `--restore`（非默认）+ 主机无 python3。生产有 python3（deploy_prod.py 依赖它，我也用它做过 yaml 解析），故判非阻塞。

### R2（外观）外科式摘除后 `plugins.configs:` 会留成空映射（`configs:` 后无子项）
合法 YAML（解析为 null），CPA 容忍；仅观感问题。若想去掉，可再补一步「若 `configs` 为空则整段删除」。

### R3（设计取舍）锚点守卫是**全局保守**的
任一 provider 的 models 出现 anchor/alias，**整批**（含完全干净的 provider）都不再同步。安全性优先，但意味着「一旦有人给 models 加了锚点，同步功能静默失效」。建议在 `/status` 或日志里给出显式告警，避免长期静默不工作。

### R4（测试工件，非缺陷）
`audit_review_test.go:159` 仍报 `encode dropped the 2nd YAML document`，但那是**直接调用 `doc.encode()`**、绕过加载器的用例。真实路径已被 `decodeSingleDocument` 拦下（`audit_v2_test.go:108`：`config file contains 2 YAML documents; refusing…`，文件未动）。此用例已失效，不代表缺陷。

## v3-5 全量回归

`go test -run 'TestAudit|TestV2_|TestV3_'` → **23 PASS / 0 FAIL**（`recon/raw/audit-v3-regression-0.1.3.txt`）。
覆盖：上游 401/非 JSON/空 body/数组/截断、重复 id 取首条、缺 id 忽略、非目标 provider 字节级零改动、`overwrite_fields` 覆盖 display-name 且保留 thinking、TOCTOU 冲突检测 + 人工改动保留、6 类异常类型产出合法 YAML、pathAllowed 10 路径矩阵、深度 fail-closed、多文档拒绝、`disabled` 全布尔写法、锚点四种形态。

## v3-6 生产只读核验

| 检查点 | 实测 | 结论 |
|---|---|---|
| `.so` | `model-meta-sync-v0.1.3.so` `1fda55de…9ff6` | ✅ |
| 0.1.2 已清理 | journal `old plugin file removed … version=0.1.2` | ✅ |
| 插件加载 | journal 15:12:23 `plugin loaded … version=0.1.3` + `plugin registered … version=0.1.3` | ✅ |
| config sha256 | `aea6afa8…3813`（与 0.1.2 时一致） | ✅ 0.1.3 未写盘 |
| 备份 | 5 个：`sync-132726`、`deploy-132722`(pre-install)、`deploy-133930`、`deploy-150014`、`deploy-151223` | ✅ 与告称一致 |
| providers / Magpie | config 未变，沿用 v2 核验：9 / 12 | ✅ |

## v3-7 未覆盖（沿用，不因终审而勾掉）

- **U3** `/v1/models` 31 条未独立取得（无客户端 key，`/v1/models` 401）。
- **U4** 未在 build-host 真实植入 `.so` 跑宿主级端到端（沙箱 CPA 进程已停）；本轮为「冻结源码 + httptest 直驱 + build-host 脚本五组实跑 + 生产只读」四重交叉。
- **U5** `deploy_prod.py` 未执行（重跑会改生产）。

## v3-8 复现命令

```bash
# 回滚脚本五组（build-host，不重启）
scp recon/raw/src-frozen-20261030-151337/rollback_prod.sh build-host:/tmp/rollback_0.1.3.sh
ssh build-host 'chmod +x /tmp/rollback_0.1.3.sh; D=/tmp/rb3; rm -rf $D; mkdir -p $D/plugins/linux/amd64; cd $D
  printf "plugins:\n    configs:\n        keeper:\n            enabled: true\n        model-meta-sync:\n            enabled: true\n        antigravity-priority:\n            enabled: true\naccess:\n    api-keys:\n        - alpha\n        - key-added-later\n" > config.yaml
  touch plugins/linux/amd64/model-meta-sync-v0.1.3.so
  CPA_CONFIG=$D/config.yaml CPA_PLUGIN_DIR=$D/plugins/linux/amd64 CPA_SKIP_RESTART=1 bash /tmp/rollback_0.1.3.sh; echo "EXIT=$?"'
# 期望：EXIT=0，块消失，keeper/antigravity/key-added-later 均在，.so=0

# R1 复现：--restore + 无 python3
ssh build-host 'D=/tmp/rg; rm -rf $D; mkdir -p $D/bin $D/plugins/linux/amd64; cd $D
  for b in bash sh find wc tr ls cp rm mkdir head seq sleep curl systemctl dirname date; do ln -sf "$(command -v $b)" $D/bin/$b; done
  printf "plugins:\n    configs:\n        model-meta-sync:\n            enabled: true\n" > config.yaml
  cp config.yaml withblock.yaml; touch plugins/linux/amd64/model-meta-sync-v0.1.3.so
  env -i PATH=$D/bin CPA_CONFIG=$D/config.yaml CPA_PLUGIN_DIR=$D/plugins/linux/amd64 CPA_SKIP_RESTART=1 \
    /bin/bash /tmp/rollback_0.1.3.sh --restore $D/withblock.yaml; echo "EXIT=$?"
  grep -c model-meta-sync config.yaml; find plugins/linux/amd64 -name "model-meta-sync-v*.so" | wc -l'
# 期望（修复后）：EXIT=1，块仍在，.so 仍在；实测：EXIT=0 且 .so=0

# 锚点与全量回归（arm64 本地）
export PATH=$PWD/.toolchain/go/bin:$PATH
cp -a recon/raw/src-frozen-20261030-151337 /tmp/a4 && cd /tmp/a4
rm -f SHA256SUMS FROZEN-AT.txt rollback_prod.sh deploy_prod.py
cp <repo>/recon/raw/audit_*_test.go .; cp <repo>/plugin/go.sum .
CGO_ENABLED=1 go test -run 'TestAudit|TestV2_|TestV3_' -v ./...     # 23 PASS
```

---

# v4 微复核（0.1.4）—— P1 与 R3 确认

- 范围：只验 P1 修复 + R3 告警 + 生产状态（其余沿用 v3 终审，全量回归重跑确认无回归）
- 冻结基线：`recon/raw/src-frozen-20261030-152140/`
  - `sync.go` `083ed52d…a55b`｜`main.go` `f55151b7…9ba3`｜`rollback_prod.sh` `4ac78486…e460`
- 生产：`.so` v0.1.4 `e685e886…18f0`｜config `aea6afa8…3813`
- 原始输出：`recon/raw/audit-v4-regression-0.1.4.txt`、`recon/raw/audit_v4_test.go`

## 终审结论（维持）：**可上线**

P1 已覆盖并实测闭环；R3 告警已接入且位置正确。全量回归 **25 PASS / 0 FAIL**。
无阻塞项。残余仅 2 条外观/重复度问题（P2），不构成上线阻碍。

## v4-1 P1 复现（build-host，屏蔽 python3，两分支）

构造：`$D/bin` 只放 bash/sh/find/wc/tr/ls/cp/rm/… 等，**不放 python3**；`env -i PATH=$D/bin` 运行。

| 场景 | 修复前（v3 实测） | **修复后（v4 实测）** | 判定 |
|---|---|---|---|
| **T-G** `--restore <含块备份>` + 无 python3 | `EXIT=0` + `rollback simulated`，**静默删除 .so 而块仍在**（错误成功） | `refusing: cannot evaluate plugin block state in …(has_block=127)`；**`EXIT=1`**；未执行任何恢复；`.so` 仍为 1 | ✅ **P1 闭环** |
| **T-F** 外科式 + 无 python3 | `EXIT=1`（原本已 fail-closed） | `failed to strip plugin block`；`EXIT=1`；块在、`.so` 在 | ✅ 未回退 |

对应代码：`--restore` 分支 `rollback_prod.sh:73-76`、回滚后校验 `:120-123`，两处均有
`if status -ne 0 && status -ne 1 → refuse; exit 1`。另新增一处**额外**保护（:`96-101`）：`strip_block`
返回 3（未找到块）时会再调 `has_block` 复核，若块存在但不可摘除（缩进异常）→ 明示 `edit manually` 并 `exit 1`，
不再当作「已停用」放行。这是 v3 未要求的加固，方向正确。

## v4-2 R3 告警（Go 直驱）

| 用例 | 实测 | 判定 |
|---|---|---|
| V4-1 两家选中 + 共享条目锚点 | `written=false`；`warnings=[{"yaml anchors/aliases detected in provider model lists; sync skipped for every selected provider"}]`，**2 条**（每家追加一次） | ✅ 告警已接入；⚠ 重复（见 R5） |
| V4-2 干净配置 | `warnings` 为空，正常同步 | ✅ 无虚假告警 |

可见性：`RunReport.Warnings []string \`json:"warnings,omitempty"\``（`sync.go:572`），设置点 `sync.go:648`。
`/preview`、`/sync` 直接返回 `*RunReport`；`/status` 经 `statusPayload()` 的 `last_run` 字段 → **三处均可见** ✅。

## v4-3 残余项（全部 P2，非阻塞）

- **R5（新增）告警按选中 provider 重复追加**：2 家选中 → 同一条 `warnings` 出现 2 次。建议移到循环外或去重（1 行）。
- **R2（沿用）** 外科式摘除后 `plugins.configs:` 留成空映射（`configs:` 无子项），合法 YAML，仅外观。
- **R3（沿用，设计取舍）** 锚点守卫全球保守——任一 provider 的 models 出现 anchor/alias，整批（含干净 provider）停止同步。v4 已补告警，可观测性改善。
- **R4（测试工件，非缺陷）** `audit_review_test.go:159` 的「多文档被丢弃」是绕过加载器直调 `doc.encode()` 的用例；真实路径由 `decodeSingleDocument` 拒绝，文件不动。

## v4-4 全量回归

`go test -run 'TestAudit|TestV2_|TestV3_|TestV4_'` → **25 PASS / 0 FAIL**，无回归。
（v3 为 23 项 + v4 新增 2 项告警用例。）

## v4-5 生产只读核验

| 检查点 | 实测 | 结论 |
|---|---|---|
| `.so` | `model-meta-sync-v0.1.4.so` `e685e886…18f0` | ✅ 与告称一致 |
| 0.1.3 已清理 | 目录内仅 `model-meta-sync-v0.1.4.so`；journal `old plugin file removed … version=0.1.3` | ✅ |
| 插件加载 | journal 15:19:53 `plugin loaded … version=0.1.4` + `plugin registered … version=0.1.4` | ✅ |
| config sha256 | `aea6afa8…3813`（与 0.1.2/0.1.3 时一致） | ✅ 0.1.4 未写盘 |
| 备份 | **6 个**（5 deploy + 1 sync），`…-deploy-20261030-132722`（pre-install）仍在 | ✅ |
| **`/v1/models` 条数** | 我用 `access.api-keys[0]` 亲自取到：**`count=31`** | ✅ **U3 关闭** |

> 更正说明：v1–v3 我一直取不到该计数，是因为我把 key 路径写成了 `api-keys.access`（错序）；v8 布局下正确路径是
> `access.api-keys`。这是我取样面的错误，非产品或运维问题。已在 v4 独立复核。

## v4-6 未覆盖（仅剩 2 项）

- **U4** 未在 build-host 真实植入 `.so` 跑宿主级端到端（沙箱 CPA 进程已停）。四轮累计为「冻结源码 + httptest 直驱 + build-host 脚本多组实跑 + 生产只读（含 journal 与 /v1/models）」四重交叉。
- **U5** `deploy_prod.py` 未执行（重跑会改生产）。

## v4-7 U3 关闭后的覆盖状态

| 早前未覆盖项 | 状态 |
|---|---|
| U1 0.1.x 是否已加载 | ✅ v2 关闭（journal `loaded`/`registered` 行；此前误查 `main.log`） |
| U3 `/v1/models` 31 / 9 providers / 12 Magpie | ✅ v4 关闭（`count=31` 亲自取到；providers/Magpie 数由 config 解析） |
| U4 / U5 | 仍未覆盖（见上） |

## v4-8 复现命令

```bash
# P1：两分支在无 python3 下的行为（build-host，不重启）
scp recon/raw/src-frozen-20261030-152140/rollback_prod.sh build-host:/tmp/rollback_0.1.4.sh
ssh build-host 'chmod +x /tmp/rollback_0.1.4.sh; D=/tmp/rb4p; rm -rf $D; mkdir -p $D/bin $D/plugins/linux/amd64; cd $D
  for b in bash sh find wc tr ls cp rm mkdir head seq sleep curl systemctl dirname date grep; do
    p=$(command -v $b); [ -n "$p" ] && ln -sf "$p" $D/bin/$b; done
  printf "plugins:\n    configs:\n        model-meta-sync:\n            enabled: true\n" > withblock.yaml
  touch plugins/linux/amd64/model-meta-sync-v0.1.4.so
  env -i PATH=$D/bin CPA_CONFIG=$D/config.yaml CPA_PLUGIN_DIR=$D/plugins/linux/amd64 CPA_SKIP_RESTART=1 \
    /bin/bash /tmp/rollback_0.1.4.sh --restore $D/withblock.yaml; echo "EXIT=$?"'
# 期望且实测：EXIT=1，refusing: cannot evaluate plugin block state (has_block=127)，.so 仍在

# R3 告警 + 全量回归（arm64 本地）
export PATH=$PWD/.toolchain/go/bin:$PATH
cp -a recon/raw/src-frozen-20261030-152140 /tmp/a5 && cd /tmp/a5
rm -f SHA256SUMS FROZEN-AT.txt rollback_prod.sh
cp <repo>/recon/raw/audit_*_test.go .; cp <repo>/plugin/go.sum .
CGO_ENABLED=1 go test -run 'TestAudit|TestV2_|TestV3_|TestV4_' -v ./...   # 25 PASS

# 生产只读（含 U3）
ssh cpa-host 'sha256sum /srv/cliproxyapi/plugins/linux/amd64/model-meta-sync-v0.1.4.so /srv/cliproxyapi/config.yaml'
ssh cpa-host 'K=$(python3 -c "
import yaml;c=yaml.safe_load(open(\"/srv/cliproxyapi/config.yaml\"))
print(((c.get(\"access\") or {}).get(\"api-keys\") or [\"\"])[0])"); \
  curl -s -H "Authorization: Bearer $K" http://127.0.0.1:8317/v1/models | \
  python3 -c "import json,sys;print(len(json.load(sys.stdin).get(\"data\",[])))"'
```
