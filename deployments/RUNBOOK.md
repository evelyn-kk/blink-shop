# 运维手册：发布、备份恢复、回滚与故障处理

适用于生产部署（API 镜像 `backend/Dockerfile` + 托管或自建的 MySQL / Nacos / MinIO / Milvus）。本地开发用的 `docker-compose.yml` 不用于生产。
每一节都在 10.1 做过演练，记录见 [`docs/10-release-readiness.md`](../docs/10-release-readiness.md)。

## 发布

1. **发布前**：CI 全绿（含 “Security & Monitoring”）；确认 `deployments/monitoring/alerts.yml` 已加载、`/metrics` 能抓到；
   在预发环境跑一次 `go run ./cmd/loadtest`（见 quality/README）。
2. **备份**：按下面“备份”做一次 MySQL 全量备份，记下文件名和 sha256。
3. **迁移**（只追加，不改不删已有列）：
   ```bash
   docker run --rm -e MYSQL_DSN=... --entrypoint /migrate blink-shop-api:<版本>          # 只列出待执行的迁移
   docker run --rm -e MYSQL_DSN=... --entrypoint /migrate blink-shop-api:<版本> -apply   # 执行（命名锁，多次执行安全）
   ```
   镜像的 ENTRYPOINT 是 `/api`，必须用 `--entrypoint /migrate`；`/api` 收到不认识的参数会报错退出，不会误启动服务。
   迁移没做完时新版本 `/ready` 返回 503“数据库还有 N 个迁移未执行”，负载均衡不会把流量切过去。
4. **滚动替换实例**：生产配置 `APP_ENV=production`、`RUN_MIGRATIONS=false`（默认）、`SHUTDOWN_DRAIN=5s`。
   旧实例收到 SIGTERM 后先让 `/ready` 返回 503，等 `SHUTDOWN_DRAIN` 让负载均衡摘掉，再停导购运行（标记 `server_shutdown`）、关闭 HTTP。
   负载均衡的健康检查用 `/api/v1/ready`，存活检查用 `/api/v1/health`（容器里用 `/api -healthcheck`，镜像没有 curl）。
   前面有 nginx 时用 `deployments/app/nginx.conf` 的写法：上游连接池（`keepalive`，否则高并发时本地端口耗尽）和
   `server api:8080 resolve`（API 换 IP 后自动重新解析，不用重载 nginx）。
5. **发布后 15 分钟**：看 5xx 比例、接口 P95、结算成功率、导购失败率、熔断状态；任何一项告警就按“回滚”处理。

## 备份

```bash
# MySQL：一致性快照（InnoDB），不锁表
mysqldump -h <host> -u <备份账号> -p --single-transaction --routines --triggers --set-gtid-purged=OFF blink_shop > blink_shop-$(date +%Y%m%d-%H%M).sql
shasum -a 256 blink_shop-*.sql
# MinIO：私有文件（上传的图片、文档原件）
mc mirror --overwrite prod/blink-shop backup/blink-shop-$(date +%Y%m%d)
```

- 头像在 `AVATAR_UPLOAD_DIR`（容器里 `/data/avatar`，挂载卷），按卷快照备份。
- Milvus 向量是可重建的派生数据（`go run ./cmd/vectorindex` 从数据库重建），不需要备份；Nacos 里的动态配置和 Prompt 镜像以数据库 / 环境变量为准，导出一份备查即可。
- **备份要验证**：定期把备份恢复到临时库，逐表 `CHECKSUM TABLE` 和源库比对（10.1 演练：26 张表全部一致），再用新版本 API 连临时库确认 `/ready` 200、能下单。

## 恢复

```bash
mysql -h <host> -u root -p -e "CREATE DATABASE blink_shop_restore CHARACTER SET utf8mb4"
mysql -h <host> -u root -p blink_shop_restore < blink_shop-<时间>.sql
docker run --rm -e MYSQL_DSN=.../blink_shop_restore --entrypoint /migrate blink-shop-api:<版本>     # 确认迁移状态
```

确认数据后切换 `MYSQL_DSN`（或改库名）并滚动重启。恢复点之后的订单、支付需要按审计日志和支付回调对账补回。

## 回滚

- **代码回滚**：直接部署上一个版本的镜像，**不回滚数据库**。迁移只追加（新列都有默认值），旧版本能在新结构上运行；
  旧版本只认识自己知道的迁移，`/ready` 照常就绪（10.1 演练：8.4 版本跑在 0008 结构上，下单、导购都正常）。
- 回滚的实例必须 `RUN_MIGRATIONS=false`（生产默认）：旧版本发现数据库里有它不认识的迁移会拒绝迁移并在日志里反复报错（不影响服务）。
- 需要撤销一个迁移时，写一个新的“反向”迁移随下个版本发布，不要手工改表或删 `schema_migrations` 记录。
- **数据回滚**（极少用，会丢数据）：按“恢复”还原到发布前的备份；先停写（摘掉所有实例），恢复后对账。

## 故障处理

### API 不可用

`BlinkAPIDown`：实例拉不到指标。看实例是否存活（`/api/v1/health`）、最近一次部署、OOM / 重启记录。`/health` 200 但 `/ready` 503 时看响应里的原因
（数据库连不上 / 迁移没做完 / 实例正在下线）。

### 5xx 升高

`BlinkHigh5xxRatio`：按 `route` 看是哪个接口（`sum by (route) (rate(blink_http_requests_total{code="5xx"}[5m]))`），日志里按 `request_id` 查 `level=ERROR`。
最近有发布就先回滚再查。数据库相关的 5xx 见“数据库”。

### 接口变慢

`BlinkCatalogSlow` / `BlinkAPISlow`：按 `route` 看 P95；同时看 `blink_db_wait_seconds` 是否在涨（连接池不够：调大 `MYSQL_MAX_OPEN_CONNS`，所有实例之和不能超过 MySQL `max_connections`）
和数据库慢查询日志。10.1 压测时连接池从 20 调到 50，等待次数从 29.8 万降到 2 千。

### 导购变慢或失败

`BlinkAgentFirstEventSlow` / `BlinkAgentFailing`：看 `blink_external_calls_total{dependency="llm"}` 和 `blink_breaker_open{dependency="llm"}`。
模型服务连续失败时熔断会打开，导购自动退回规则回答（运行仍是 completed，内容是规则生成的）。模型长期不可用可以在后台关掉
`ai.agent_enabled` / `ai.planner_enabled`。管理端“调试观测”页按状态筛选 failed 的运行，看时间线里哪一步出错。

### 结算失败

`BlinkCheckoutFailing`（不含库存不足、价格变化等业务拒绝）：看 `blink_checkout_total{result="internal_error"}` 和数据库错误日志。
结算是幂等的（`Idempotency-Key`），客户端重试不会重复下单；不要手工改订单状态，按审计日志处理。

### 外部依赖故障

`BlinkDependencyErrors` / `BlinkBreakerOpen` / `BlinkVectorSyncFailing`，按 `dependency` 处理：

| 依赖 | 影响 | 处理 |
| --- | --- | --- |
| llm | 导购退回规则回答 | 检查模型服务、密钥、额度；熔断 30 秒后自动探测恢复 |
| embedding / milvus | 检索退回关键词，图搜返回 503，商品向量同步失败（检索结果仍按可见性过滤，不会推荐下架商品） | 恢复后跑 `cmd/vectorindex` 补齐，或等商品变更时增量同步 |
| nacos | 动态配置沿用最后一次读到的值，后台改配置返回 503 | 恢复后自动按刷新间隔读取；Prompt 运行时以数据库为准，不受影响 |
| minio | 上传、私有文件下载返回 503，浏览和交易不受影响 | 恢复后无需处理 |
| stt / tts | 语音输入 / 朗读不可用 | 前端已提示，恢复后无需处理 |

### 数据库

MySQL 重启或主从切换期间 `/ready` 503、写接口失败；数据库恢复后连接池自动重连，不需要重启 API（10.1 演练：MySQL 重启 35 秒，
数据库报 ready 的同一秒 API 恢复）。`BlinkDBPoolSaturated` 见“接口变慢”。
