# model-meta-sync 上线与运维手册

生产：`<cpa-host>`（CPA v8，`<cpa-dir>`，systemd **user** 单元 `cliproxyapi.service`）
沙箱/预发：本地 arm64 `.sandbox/`、`<build-host>` amd64 `/tmp/mms-stage/`

`deploy_prod.py` / `rollback_prod.sh` 的默认路径是 `/srv/cliproxyapi`；实际目录不同时用环境变量覆盖：

```bash
export CPA_DIR=/your/cpa/dir          # deploy_prod.py
export CPA_CONFIG=/your/cpa/config.yaml   # rollback_prod.sh
```

## 1. 当前生产状态（0.2.0）

| 项 | 值 |
|---|---|
| 插件 | `plugins/linux/amd64/model-meta-sync-v0.2.0.so`（旧版本由 host 清理） |
| 作用范围 | 配置内**全部启用的** openai-compatibility provider（不再有 `providers` 白名单；该键仅作可选过滤） |
| 配置块 | `plugins.configs.model-meta-sync`（mode=missing，未设 `overwrite_fields`，sync_on_start=true，dry_run_on_start=false） |
| 首轮铺开 | 先 `MMS_DRY_RUN_ON_START=true` 部署，仅报告：7 家取到列表、1 家（`/v2` 根路径）404 被隔离；确认后改 false 应用，写入 12 处空字段，Magpie 已填值零改动 |
| 幂等 | 应用后再次启动同步 `changed_fields=0`、未写盘 |
| 备份 | deploy：`config.yaml.bak-modelmetasync-deploy-<ts>`；插件写前：`config.yaml.bak-modelmetasync-<ts>`（deploy 备份不会被 prune） |

## 2. 安装/升级

```bash
# 1) 预发复验（可选，amd64 主机上的沙箱）
scp model-meta-sync-v0.2.0.so <build-host>:/tmp/
ssh <build-host> 'cd /tmp/mms-stage/.sandbox && export SANDBOX_PORT=18318 MOCK_BASE=http://127.0.0.1:19002 SANDBOX_BASE=http://127.0.0.1:18318 && ./run_suite.sh'

# 2) 首次铺开：只报告不落盘
scp model-meta-sync-v0.2.0.so deploy_prod.py <cpa-host>:/tmp/
ssh <cpa-host> 'cd /tmp && MMS_DRY_RUN_ON_START=true CPA_DIR=/your/cpa/dir python3 deploy_prod.py /tmp/model-meta-sync-v0.2.0.so'
ssh <cpa-host> 'export XDG_RUNTIME_DIR=/run/user/0; journalctl --user -u cliproxyapi --since "-3min" --no-pager | grep "\[model-meta-sync\]"'

# 3) 确认报告后应用
ssh <cpa-host> 'cd /tmp && MMS_DRY_RUN_ON_START=false CPA_DIR=/your/cpa/dir python3 deploy_prod.py /tmp/model-meta-sync-v0.2.0.so'
```

升级 = 覆盖同名 `.so` + `systemctl --user restart cliproxyapi`（插件 host 只在启动时扫描，不能热加载新 .so；只改插件配置/数据不需要重启）。

## 3. 手动触发/预览（需要管理密钥）

管理面鉴权：`X-Management-Key: <明文密钥>` 或 `Authorization: Bearer <明文密钥>`（配置里只存 bcrypt 哈希）。

```bash
KEY=...   # CPA 管理面板密码
# 预览（不落盘）
curl -s -H "X-Management-Key: $KEY" http://127.0.0.1:8317/v0/management/model-meta-sync/preview | jq .
# 应用
curl -s -X POST -H "X-Management-Key: $KEY" http://127.0.0.1:8317/v0/management/model-meta-sync/sync | jq '{ok,changed_fields,written,backup,providers:[.providers[]|{name,matched,changed_models,upstream_error}]}'
# 只同步某一家（按供应商名称）
curl -s -X POST -H "X-Management-Key: $KEY" 'http://127.0.0.1:8317/v0/management/model-meta-sync/sync?provider=GatewayA' | jq .
# 最近一次结果
curl -s -H "X-Management-Key: $KEY" http://127.0.0.1:8317/v0/management/model-meta-sync/status | jq .
```

语义：
- `missing`（当前）：只补空字段；`overwrite_fields` 里列出的字段才允许覆盖（缺省 = 不覆盖）。
- `always`：`fields` 全部按上游重算。**慎开 thinking**：CPA 对不支持档位就近 clamp（平局取低档），删掉上游没有的 `medium` 会让显式请求 medium 的客户端降到 low。
- 单个上游失败 → 该 provider 记 `upstream_error`，其余照常写；全部失败 → HTTP 502、不写文件。
- provider 名匹配不到 → 400。

## 4. 改行为（不重启 CPA）

编辑 `plugins.configs.model-meta-sync`（管理面板插件配置或直接改 `config.yaml`）会触发热重载，插件重新注册。
常用改动：
- 只看某几家：`providers: [GatewayA, GatewayB]`（按名称精确匹配；留空 = 全部）。
- 连 disabled 的 provider 一起同步：`include_disabled: true`。
- 把 thinking 纳入覆盖：`overwrite_fields: [thinking]`。
- 周期性同步：`interval_minutes: 360`（0=关）。
- 关闭启动同步：`sync_on_start: false`；启动只预览：`dry_run_on_start: true`。

> 注意：改本插件自己的配置块会触发 CPA 重载，插件会重新初始化；数据文件不会因此被改写（幂等）。

## 5. 回滚

```bash
# 恢复最近一次部署前配置 + 删除 .so + 重启
scp rollback_prod.sh <cpa-host>:/tmp/ && ssh <cpa-host> 'chmod +x /tmp/rollback_prod.sh && CPA_CONFIG=/your/cpa/config.yaml /tmp/rollback_prod.sh'
# 或指定备份
ssh <cpa-host> '/tmp/rollback_prod.sh /your/cpa/config.yaml.bak-modelmetasync-deploy-<ts>'
```

只停用不改文件：

```bash
ssh <cpa-host> 'python3 - <<PY'
import yaml
p="/your/cpa/config.yaml"
d=yaml.safe_load(open(p))
d["plugins"]["configs"]["model-meta-sync"]["enabled"]=False
open(p,"w").write(yaml.safe_dump(d, sort_keys=False, allow_unicode=True))
PY
export XDG_RUNTIME_DIR=/run/user/0; systemctl --user restart cliproxyapi'
```

## 6. 排障

| 现象 | 查什么 |
|---|---|
| 启动日志无 `plugin loaded ... model-meta-sync` | .so 是否在 `plugins/<os>/<arch>/`、权限 644、ABI/schema 是否匹配（日志会写 load 失败原因） |
| 同步返回 502 | 看 `providers[].upstream_error`（地址已打码，用 provider 名称定位）；在该 provider 的 base-url 上复现 `/v1/models` |
| 某家一直 `matched=0/0` + 404 | 该网关模型列表不在 `<base-url>/v1/models`（例如根路径是 `/v2`）；暂无逐 provider 路径覆盖，跳过即可 |
| 返回 `config changed while syncing; retry` | 有人同时在管理面板保存；重试即可（不会覆盖） |
| 写后没有热重载 | 确认是原地写（插件已实现）；若文件被外部改成 rename 写入，watcher 只认 Write/Create/Rename 事件 |
| 报告 `unmatched` | 条目的 `name` 与上游 id 连宽松规则都对不上（大小写、`/` 前缀、分隔符、`-exp/-preview` 后缀、`-self/-official` 部署限定词都已覆盖） |
| 看不到部分模型 | alias 撞名（CPA 全局按 alias 去重），需 provider `prefix` 或唯一 alias，与本插件无关 |

## 7. 已知边界

- 输出上限（`max_output_tokens`）、`native_endpoints`、`pricing`、`service_tiers` 在 CPA 配置里没有落点，不会同步。
- 插件不增删模型、不改 alias/name/base-url/keys；语义 diff 校验会拒绝任何越界改动。
- 插件安装/升级需重启一次 CPA；数据同步走热重载。
