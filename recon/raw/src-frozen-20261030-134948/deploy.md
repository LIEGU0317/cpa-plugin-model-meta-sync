# model-meta-sync 上线与运维手册

生产：`cpa-host`（CPA v8.0.47，`/srv/cliproxyapi`，systemd **user** 单元 `cliproxyapi.service`）
沙箱/预发：本地 arm64 `.sandbox/`、build-host amd64 `/tmp/mms-stage/`

## 1. 当前生产状态（2026-10-30 13:27 CST）

| 项 | 值 |
|---|---|
| 插件 | `plugins/linux/amd64/model-meta-sync-v0.1.1.so` sha256 `d0c125d7…eb51bf`（0.1.0 已被 host 清理） |
| 配置块 | `plugins.configs.model-meta-sync`（mode=missing，overwrite_fields=display-name/max-context-length/input-modalities/output-modalities，sync_on_start=true） |
| 首次运行 | 0.1.0 上线时启动后 3s 自动同步，写入 3 条 `max-context-length`(1000000→1048576)，别名/条目/其它 provider 未变；0.1.1 复部署检测 0 变更、未写文件 |
| 备份 | `/srv/cliproxyapi/config.yaml.bak-modelmetasync-deploy-20261030-132722`（部署前）、`config.yaml.bak-modelmetasync-20261030-132726`（插件写前） |
| 网关 | `/v1/models` 31 条（与上线前一致），进程稳定 |

## 2. 安装/升级

```bash
# 1) 预发复验（build-host）
scp model-meta-sync-v0.1.1.so build-host:/tmp/
ssh build-host 'cd /tmp/mms-stage/.sandbox && export SANDBOX_PORT=18318 MOCK_BASE=http://127.0.0.1:19002 SANDBOX_BASE=http://127.0.0.1:18318 && ./run_suite.sh'

# 2) 生产部署（会备份 + 插入配置块 + 重启 user 服务 + 等自动同步）
scp model-meta-sync-v0.1.1.so deploy_prod.py cpa-host:/tmp/
ssh cpa-host 'cd /tmp && python3 deploy_prod.py /tmp/model-meta-sync-v0.1.1.so'

# 3) 校验
ssh cpa-host 'export XDG_RUNTIME_DIR=/run/user/0; journalctl --user -u cliproxyapi -n 80 --no-pager | grep -E "model-meta-sync|reloaded"'
```

升级 = 覆盖同名 `.so` + `systemctl --user restart cliproxyapi`（插件 host 只在启动时扫描，不能热加载新 .so；只改插件配置/数据不需要重启）。

## 3. 手动触发/预览（需要管理密钥）

管理面鉴权：`X-Management-Key: <明文密钥>` 或 `Authorization: Bearer <明文密钥>`（生产配置里是 bcrypt 哈希，明文只在你自己手里）。

```bash
KEY=...   # CPA 管理面板密码
# 预览（不落盘）
curl -s -H "X-Management-Key: $KEY" http://127.0.0.1:8317/v0/management/model-meta-sync/preview | jq .
# 应用
curl -s -X POST -H "X-Management-Key: $KEY" http://127.0.0.1:8317/v0/management/model-meta-sync/sync | jq '{ok,changed_fields,written,backup,providers:[.providers[]|{name,matched,changed_models,upstream_error}]}'
# 只同步某一家
curl -s -X POST -H "X-Management-Key: $KEY" 'http://127.0.0.1:8317/v0/management/model-meta-sync/sync?provider=Magpie' | jq .
# 最近一次结果
curl -s -H "X-Management-Key: $KEY" http://127.0.0.1:8317/v0/management/model-meta-sync/status | jq .
```

语义：
- `missing`（当前）：只补空字段；`overwrite_fields` 里的字段允许覆盖。
- `always`：`fields` 全部按上游重算。**慎开 thinking**：CPA 对不支持档位就近 clamp（平局取低档），删掉上游没有的 `medium` 会让显式请求 medium 的客户端降到 low。
- 上游失败 → HTTP 502、`ok:false`、不写文件；provider 名匹配不到 → 400。

## 4. 改行为（不重启 CPA）

编辑 `plugins.configs.model-meta-sync`（管理面板插件配置或直接改 `config.yaml`）会触发热重载，插件重新注册。
常用改动：
- 加 provider：`providers: [Magpie, OtherGateway]`（要求同为 OpenAI 兼容且 `/v1/models` 带参数）。
- 把 thinking 纳入覆盖：`overwrite_fields` 追加 `thinking`。
- 周期性同步：`interval_minutes: 360`（0=关）。
- 关闭启动同步：`sync_on_start: false`。

> 注意：改本插件自己的配置块会触发 CPA 重载，插件会重新初始化；数据文件不会因此被改写（幂等）。

## 5. 回滚

```bash
# 恢复最近一次部署前配置 + 删除 .so + 重启
scp rollback_prod.sh cpa-host:/tmp/ && ssh cpa-host 'chmod +x /tmp/rollback_prod.sh && /tmp/rollback_prod.sh'
# 或指定备份
ssh cpa-host '/tmp/rollback_prod.sh /srv/cliproxyapi/config.yaml.bak-modelmetasync-deploy-20261030-132722'
```

只停用不改文件：

```bash
ssh cpa-host 'python3 - <<PY'
import yaml
p="/srv/cliproxyapi/config.yaml"
d=yaml.safe_load(open(p))
d["plugins"]["configs"]["model-meta-sync"]["enabled"]=False
open(p,"w").write(yaml.safe_dump(d, sort_keys=False, allow_unicode=True))
PY
export XDG_RUNTIME_DIR=/run/user/0; systemctl --user restart cliproxyapi'
```

## 6. 排障

| 现象 | 查什么 |
|---|---|
| 启动日志无 `plugin loaded ... model-meta-sync` | .so 是否在 `plugins/linux/amd64/`、权限 644、ABI/schema 是否匹配（日志会写 load 失败原因） |
| 同步返回 502 | `providers[].upstream_error`；从 CPA 主机 `curl -H "Authorization: Bearer <key>" <base-url>/v1/models` 复现 |
| 返回 `config changed while syncing; retry` | 有人同时在管理面板保存；重试即可（不会覆盖） |
| 写后没有热重载 | 确认是原地写（插件已实现）；若文件被外部改成 rename 写入，watcher 只认 Write/Create/Rename 事件 |
| 报告 `unmatched` | CPA 条目的 `name` 与上游 id 不完全一致（注意大小写与 `auto/` 前缀）；`GET /v1/models/{id}` 只认列表 id |
| 看不到 Magpie 的部分模型 | alias 撞名（CPA 全局按 alias 去重），需 provider `prefix` 或唯一 alias，与本插件无关 |

## 7. 已知边界

- 输出上限（Magpie `max_output_tokens`）、`native_endpoints`、`pricing`、`service_tiers` 在 CPA 配置里没有落点，不会同步。
- 插件不增删模型、不改 alias/name/base-url/keys；语义 diff 校验会拒绝任何越界改动。
- 插件安装/升级需重启一次 CPA；数据同步走热重载。
