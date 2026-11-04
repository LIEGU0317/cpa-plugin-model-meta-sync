# 插件标准合规验证 — 0.3.1

时间：2026-11-04
目标：确认 0.3.1 的产物与登记信息符合 CPA（CLIProxyAPI v8.0.47）插件商店标准，并且插件在真实宿主上仍能完成同步。

标准来源：`router-for-me/CLIProxyAPI-Plugins-Store` 的 `README.md`（registry 字段、release 资产命名、
zip 布局、checksums 格式）与 CPA 源码 `internal/pluginstore/{registry,install,manifest}.go`、
`sdk/pluginapi/types.go`（`Metadata.Logo` / `Metadata.ConfigFields`）。

## 1. 发布产物布局

`v0.3.1` 由 `.github/workflows/build.yml` 在 tag 上构建并发布（5 个平台全部成功）：

```text
$ gh run view 41791952456
✓ Test            ✓ Build darwin/amd64   ✓ Build linux/arm64
✓ Build linux/amd64  ✓ Build darwin/arm64  ✓ Build windows/amd64   ✓ Release

$ gh release view v0.3.1 --json assets
checksums.txt                          524 bytes
model-meta-sync_0.3.1_darwin_amd64.zip   2989123
model-meta-sync_0.3.1_darwin_arm64.zip   2756322
model-meta-sync_0.3.1_linux_amd64.zip    3136251
model-meta-sync_0.3.1_linux_arm64.zip    2846600
model-meta-sync_0.3.1_windows_amd64.zip  3058752

$ cat checksums.txt
b97a42d07d4b50285641d9ecb0c2f5cf3067b6e51e0fd839ee586a8f37af41a4  model-meta-sync_0.3.1_darwin_amd64.zip
0b73039229310dfd9aacabcb6e2fdd1dba6b48a4dcb9a83f6d2b472626f66594  model-meta-sync_0.3.1_darwin_arm64.zip
078d81a2596e03a8e0e63890face121922ed0773729868f66e016cbffa2c9391  model-meta-sync_0.3.1_linux_amd64.zip
2fc0486c0e0309fcd6ba8fe21fdf6cc3e6dd5a20f9cca77311cfe37780266c13  model-meta-sync_0.3.1_linux_arm64.zip
b25f8b523cbc6402f8d8c48ed84f013e73df45372dcfcdb420c44d0319df2a0e  model-meta-sync_0.3.1_windows_amd64.zip

$ unzip -l model-meta-sync_0.3.1_linux_amd64.zip
  Length      Date    Time    Name
---------  ---------- -----   ----
  7720288  1980-01-01 08:00   model-meta-sync.so
---------                     -------
  7720288                     1 file
```

zip 根目录仅一个动态库、文件名等于 `<id>.<ext>`、无嵌套路径 —— 与 `internal/pluginstore/install.go`
`readTargetLibrary` 的接受条件一致；`plugin/cmd/zipdist` 的 `-verify` 在 CI 的每个平台任务里都会
重复这一检查，`-checksums` 在 Release 任务里生成 `checksums.txt`。

仓库内跟踪的 `plugins/linux/amd64/model-meta-sync-v0.3.1.so` 就是发布 zip 内的同一份库
（sha256 `5e29f03ddfc273e54cb4e9ee38f689ac15364a21a96a89259a6e39aaf3f044b3`）。本机交叉编译
（`CC=x86_64-linux-gnu-gcc`）产出与 CI（ubuntu-24.04 原生 gcc）只有 C 工具链不同、源码与 Go
工具链相同，因此字节不同；对外以 release 资产为准。

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
    install ok: .../001/linux/amd64/model-meta-sync-v0.3.1.so (7720288 bytes)
--- PASS: TestModelMetaSyncArchiveInstalls (0.14s)
PASS
ok  github.com/router-for-me/CLIProxyAPI/v8/internal/pluginstore  0.158s
```

- `ParseRegistry` 接受 `store/registry-entry.json`（包在 `{"schema_version":1,"plugins":[...]}` 里）。
- `InstallArchive` 把**下载自 release 的** `model-meta-sync_0.3.1_linux_amd64.zip` 安装为
  `plugins/linux/amd64/model-meta-sync-v0.3.1.so`（商店安装的最终路径与文件名）；本机构建的
  `..._linux_arm64.zip` 同样通过。
- 同一测试确认旧的「裸 `.so` 资产」不是可安装归档（`InstallArchive` 拒绝非 zip 输入）。

## 3. 真实宿主端到端（CPA v8.0.47 沙箱，linux/arm64）

宿主：由 `v8.0.47` 源码 `go build ./cmd/server` 构建；插件：本机 arm64 构建的
`model-meta-sync-v0.3.1.so`（与发布 zip 同源）；
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


## 5. 平台冒烟：每个平台的产物都用同平台真实宿主加载过

`scripts/smoke-test.sh` 在目标平台上下载官方 CLIProxyAPI 二进制，把构建出的归档装进
`plugins/<goos>/<goarch>/`，然后断言：注册元数据（版本 / logo / 14 条 config_fields）、mock 上游同步
真实写盘、`config successfully reloaded`、二次同步 `written=false`。CI 运行
`41795553847`、`41796172714` 的结果：

| 平台 | 构建 runner | 真实宿主加载 | 结果 |
|---|---|---|---|
| linux/amd64 | ubuntu-24.04 | CLIProxyAPI 8.0.53 linux_amd64 | PASS：注册 + 同步 + 热重载 + 幂等 |
| linux/arm64 | ubuntu-24.04-arm | CLIProxyAPI 8.0.53 linux_aarch64 | PASS：同上 |
| windows/amd64 | windows-2025 | CLIProxyAPI 8.0.53 windows_amd64 | PASS：同上（首次因 mock 未就绪 502，已加就绪等待） |
| darwin/arm64 | macos-15 | CLIProxyAPI 8.0.53 darwin_aarch64 | PASS：同上 |
| darwin/amd64 | macos-15-intel | CLIProxyAPI 8.0.53 darwin_amd64 | FAIL：宿主在 `callRegister` 内崩溃 |

上表来自「构建产物」冒烟（run `41795553847` / `41797631780`）。此外 `41798510754`
（`workflow_dispatch -f release_tag=v0.3.1`）直接对**已发布的 release 资产**做同一套冒烟：每个平台任务
先 `gh release download` 取回 `model-meta-sync_0.3.1_<goos>_<goarch>.zip` 与 `checksums.txt`，按对应行
校验 sha256（日志 `model-meta-sync_0.3.1_linux_amd64.zip: OK`），再装进官方宿主运行。结论与上表一致：
linux/amd64、linux/arm64、windows/amd64、darwin/arm64 PASS，darwin/amd64 仍是宿主限制。

darwin/amd64 的失败不是本插件的产物问题，而是 macOS Intel 上的宿主限制。用官方插件对照
（probe workflow，`macos-15-intel` + CLIProxyAPI 8.0.53 darwin_amd64 + 官方
`antigravity-priority_1.2.23_darwin_amd64.zip`）：

```text
[warn] pluginhost: failed to load plugin antigravity-priority from
       plugins/darwin/amd64/antigravity-priority-v1.2.23.dylib: dlopen ...:
       preflight probe failed (exit status 2): fatal error: addspecial on invalid pointer
```

本插件的崩溃点是同一机制的另一半：

```text
runtime: g 37: unexpected return pc for runtime.cgocallback called from 0x104625368
  ... _cgoexp_8bb65f1557b6_ModelMetaSyncPluginCall+0x0
  ... runtime.cgocallbackg / runtime.cgocallback
  ... pluginhost.callPluginRaw ... callRegister
```

两边宿主与插件都是 go1.26.10 构建（`strings` 查 `cli-proxy-api` 得 `go1.26.10`），即 darwin/amd64 上
「一个进程里两个 Go runtime」的已知冲突。因此 darwin/amd64 的冒烟任务标记为 informational
（`continue-on-error`），不阻塞发布；归档仍然发布，供宿主修复后直接可用。

## 6. 结论

0.3.1 在三个层面同时满足插件标准：注册元数据（含 `Logo` 与 14 条 `ConfigFields`）、发布产物
（zip + `checksums.txt`，被真实安装器接受）、商店登记条目（被真实 `ParseRegistry` 接受）。
功能面无回归：启动 dry-run、真实写入、热重载、幂等均与 0.3.0 文档一致。
平台层面：linux/amd64、linux/arm64、windows/amd64、darwin/arm64 由真实宿主加载验证通过；
darwin/amd64 受宿主在 macOS Intel 上无法加载 Go c-shared 插件的限制（官方插件同样失败）。
