# 插件标准合规验证 — 0.3.1

时间：2026-11-04
目标：确认 0.3.1 的产物与登记信息符合 CPA（CLIProxyAPI v8.0.47）插件商店标准，并且插件在真实宿主上仍能完成同步。

标准来源：`router-for-me/CLIProxyAPI-Plugins-Store` 的 `README.md`（registry 字段、release 资产命名、
zip 布局、checksums 格式）与 CPA 源码 `internal/pluginstore/{registry,install,manifest}.go`、
`sdk/pluginapi/types.go`（`Metadata.Logo` / `Metadata.ConfigFields`）。

## 1. 发布产物布局

```text
$ make dist GOOS=linux GOARCH=amd64 CC=x86_64-linux-gnu-gcc
$ make verify-dist GOOS=linux GOARCH=amd64
zip layout ok: ../dist/model-meta-sync_0.3.1_linux_amd64.zip (model-meta-sync.so, 3136185 bytes)

$ unzip -l dist/model-meta-sync_0.3.1_linux_amd64.zip
  Length      Date    Time    Name
---------  ---------- -----   ----
  7122288  1980-01-01 08:00   model-meta-sync.so
---------                     -------
  7122288                     1 file

$ cat dist/checksums.txt
5b213852eeaef7d4a4feb7abccff80153fcf2a0bc15c5b46c898249287e98d2d  model-meta-sync_0.3.1_linux_amd64.zip
4e016d19e112609d7e6501ee444bbdecbad706f0888abf0039604e0fba1d6d52  model-meta-sync_0.3.1_linux_arm64.zip
```

zip 根目录仅一个动态库、文件名等于 `<id>.<ext>`、无嵌套路径 —— 与 `internal/pluginstore/install.go`
`readTargetLibrary` 的接受条件一致；`plugin/cmd/zipdist` 的 `-verify` 会在 CI 中重复这一检查。

## 2. 用 CPA 真实代码校验登记条目与安装

在 CPA v8.0.47 源码树（`/tmp/cpa-src`，`git describe --tags` = `v8.0.47`）中临时加入
`internal/pluginstore/zz_modelmetasync_release_test.go` 并执行：

```text
$ go test ./internal/pluginstore -run 'TestModelMetaSync' -v
=== RUN   TestModelMetaSyncRegistryEntry
    entry ok: id=model-meta-sync name="Model Meta Sync"
      logo=https://raw.githubusercontent.com/LIEGU0317/cpa-plugin-model-meta-sync/main/assets/logo.png
      tags=[Management Models]
--- PASS: TestModelMetaSyncRegistryEntry (0.00s)
=== RUN   TestModelMetaSyncArchiveInstalls
    install ok: /tmp/TestModelMetaSyncArchiveInstalls.../001/linux/arm64/model-meta-sync-v0.3.1.so (7122288 bytes)
--- PASS: TestModelMetaSyncArchiveInstalls (0.20s)
PASS
ok  github.com/router-for-me/CLIProxyAPI/v8/internal/pluginstore  0.222s
```

- `ParseRegistry` 接受 `store/registry-entry.json`（包在 `{"schema_version":1,"plugins":[...]}` 里）。
- `InstallArchive` 把 `model-meta-sync_0.3.1_linux_arm64.zip` 安装为
  `plugins/linux/arm64/model-meta-sync-v0.3.1.so`（商店安装的最终路径与文件名）。
- 同一测试确认旧的「裸 `.so` 资产」不是可安装归档（`InstallArchive` 拒绝非 zip 输入）。

## 3. 真实宿主端到端（CPA v8.0.47 沙箱，linux/arm64）

宿主：由 `v8.0.47` 源码 `go build ./cmd/server` 构建；插件：`model-meta-sync-v0.3.1.so`；
上游：本地 mock `GET /v1/models`（2 个模型，带 display_name / context_length / modalities / reasoning levels）。

```text
[info ] pluginhost: plugin loaded     plugin_id=model-meta-sync version=0.3.1
[info ] pluginhost: plugin registered plugin_id=model-meta-sync version=0.3.1
```

`GET /v0/management/plugins`（管理密钥鉴权）返回：

```json
{
  "id": "model-meta-sync",
  "registered": true,
  "logo": "https://raw.githubusercontent.com/LIEGU0317/cpa-plugin-model-meta-sync/main/assets/logo.png",
  "metadata": {
    "name": "model-meta-sync", "version": "0.3.1", "author": "LIEGU0317",
    "github_repository": "https://github.com/LIEGU0317/cpa-plugin-model-meta-sync",
    "logo": "https://raw.githubusercontent.com/LIEGU0317/cpa-plugin-model-meta-sync/main/assets/logo.png"
  }
}
```

`config_fields` 共 14 条，类型 / 默认值 / 枚举经 RPC 序列化后完好，例如：

```text
mode              | enum    | "missing"      | enum_values=[missing always]
fields            | array   | [display-name max-context-length input-modalities output-modalities thinking]
timeout_seconds   | integer | 20
sync_on_start     | boolean | true
models_paths      | object  | {}
upstream_models_path | string | "/v1/models"  | Deprecated: prefer models_paths
```

行为链路（`dry_run_on_start: true` 启动）：

```text
$ GET  /v0/management/model-meta-sync/status
  last_run: dry_run=true, matched=2, changed_fields=10, config.yaml sha256 未变

$ POST /v0/management/model-meta-sync/sync
  ok=true written=true backup=/tmp/cpa-sandbox/run/config.yaml.bak-modelmetasync-20261104-151009
  [info] config successfully reloaded, triggering client reload

$ POST /v0/management/model-meta-sync/sync   （幂等）
  ok=true written=false changed_fields=0
```

写入结果（节选）：

```yaml
openai-compatibility:
    - name: Mock Gateway
      models:
        - display-name: DeepSeek V4.1 Flash
          input-modalities: [text]
          max-context-length: 131072
          name: deepseek-v4.1-flash
          output-modalities: [text]
          thinking:
            levels: [low, medium, high]
```

`GET /v1/models` 仍返回两个模型（目录未被破坏），`GET /v8/management/plugins` 与 v0 路由一致。

## 4. 单元测试与版本一致性

```text
$ make test
version ok: 0.3.1
ok  cpa-model-meta-sync            0.210s
ok  cpa-model-meta-sync/cmd/zipdist 0.016s

$ make vet
（无输出）
```

`plugin/metadata_test.go` 覆盖：必填元数据非空、版本为点分数字、`Logo` 为指向本仓库的 https 图片、
`ConfigFields` 与 `Config` 的 yaml 键集合互为子集、字段默认值与 `parseConfig` 一致、枚举默认值合法。

## 5. 结论

0.3.1 在三个层面同时满足插件标准：注册元数据（含 `Logo` 与 14 条 `ConfigFields`）、发布产物
（zip + `checksums.txt`，被真实安装器接受）、商店登记条目（被真实 `ParseRegistry` 接受）。
功能面无回归：启动 dry-run、真实写入、热重载、幂等均与 0.3.0 文档一致。
