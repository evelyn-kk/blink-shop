# 本地依赖（Docker Compose）

只用于本地开发。所有端口绑定 `127.0.0.1`，凭据为开发默认值；生产环境不得使用。

## 启动与停止

在仓库根目录执行：

```bash
# 启动并等待全部健康（首次需要拉镜像）
docker compose -f deployments/docker-compose.yml up -d --wait

# 查看状态
docker compose -f deployments/docker-compose.yml ps

# 停止（保留数据卷）
docker compose -f deployments/docker-compose.yml down

# 停止并清空数据（重置为空库）
docker compose -f deployments/docker-compose.yml down -v
```

Compose 项目名固定为 `blink_shop`，数据卷为 `blink_shop_<服务>_data`，不会和其他项目（包括上游 xzxg-shop）的卷冲突。重复执行 `up -d` 是幂等的：容器已存在且配置未变时不会重建。

## 端口冲突

本机已有服务占用端口时（例如本地装了 MySQL 占 3306），用环境变量覆盖宿主机端口：

```bash
BLINK_MYSQL_PORT=3307 docker compose -f deployments/docker-compose.yml up -d --wait
```

或在根目录 `.env` 里设置（见 `.env.example` 的“本地依赖容器”分组），然后加 `--env-file .env`。改了端口后，同步修改后端用的 `MYSQL_DSN` 等连接配置。

## 服务与健康检查

| 服务 | 镜像 | 宿主机端口（变量） | 容器健康检查 | 宿主机手动检查 |
| --- | --- | --- | --- | --- |
| MySQL | `mysql:8.4.11` | 3306（`BLINK_MYSQL_PORT`） | `mysqladmin ping` | `docker compose -f deployments/docker-compose.yml exec mysql mysql -ublink -pblink_dev_password -e 'select 1' blink_shop` |
| Redis | `redis:7.4.11` | 6379（`BLINK_REDIS_PORT`） | `redis-cli ping` | `docker compose -f deployments/docker-compose.yml exec redis redis-cli ping` |
| Nacos | `nacos/nacos-server:v2.5.1` | 8848 HTTP、9848 gRPC（`BLINK_NACOS_PORT`、`BLINK_NACOS_GRPC_PORT`） | `GET /nacos/v1/console/health/readiness` | `curl -f http://127.0.0.1:8848/nacos/v1/console/health/readiness` |
| MinIO | `bitnamilegacy/minio:2025.4.22` | 9000 API、9001 控制台（`BLINK_MINIO_PORT`、`BLINK_MINIO_CONSOLE_PORT`） | `GET /minio/health/live` | `curl -f http://127.0.0.1:9000/minio/health/live` |
| etcd | `quay.io/coreos/etcd:v3.5.18` | 不暴露，仅供 Milvus | `etcdctl endpoint health` | `docker compose -f deployments/docker-compose.yml exec etcd etcdctl endpoint health` |
| Milvus | `milvusdb/milvus:v2.5.10` | 19530 gRPC（`BLINK_MILVUS_PORT`） | `GET :9091/healthz`（容器内） | `docker compose -f deployments/docker-compose.yml exec milvus curl -f http://127.0.0.1:9091/healthz` |

镜像锁定：`docker-compose.yml` 中每个镜像都写成 `发行版本@sha256:<多架构 index digest>`，任何时间拉取都得到同一份镜像（同时含 amd64/arm64）。CI 会检查所有镜像都带 digest。升级时用下面的命令取新 digest，版本号和 digest 一起改：

```bash
docker buildx imagetools inspect mysql:8.4.11 | awk '/^Digest:/{print $2}'
```

| 服务 | 发行版本 | index digest |
| --- | --- | --- |
| MySQL | `mysql:8.4.11` | `sha256:6ea90827b1100f8f2ae306a539f86d2c264a26ed435a2a9f75551dd5c3aeb242` |
| Redis | `redis:7.4.11` | `sha256:c6eabf748fc7a61dbb5a705c78bcf3d6377b1127a97d0ce965c11c44ba46896f` |
| Nacos | `nacos/nacos-server:v2.5.1` | `sha256:8987908cb94ed5f9d30522a64493d35732a6c05f216d667a7addb022f3d92e80` |
| MinIO | `bitnamilegacy/minio:2025.4.22` | `sha256:50cec18ac4184af4671a78aedd5554942c8ae105d51a465fa82037949046da01` |
| etcd | `quay.io/coreos/etcd:v3.5.18` | `sha256:d0a641d5fbcc89678c931a61b7de7b8a1cf097149f135c9c73bc81d076a1494b` |
| Milvus | `milvusdb/milvus:v2.5.10` | `sha256:02e1d60d71ab60f435c60076f4fed2abe59602ecd5e18dcfe229c8c558c4379d` |

默认开发凭据：

| 用途 | 账号 | 密码 |
| --- | --- | --- |
| MySQL 业务账号（库 `blink_shop`） | `blink` | `blink_dev_password` |
| MySQL root | `root` | `blink_dev_root` |
| MinIO | `minioadmin` | `minioadmin` |
| Nacos | 鉴权关闭 | — |

## 说明

- 官方 `minio/minio` 镜像已停止公开发布，这里使用 Bitnami 归档的同版本镜像。它不再接收安全更新，只适合本地开发。
- Milvus 依赖 etcd 和 MinIO 健康后才启动，首次启动约需 1–2 分钟。
- Nacos、Redis、Milvus 在后端都是可降级依赖：不启动它们时后端仍应能运行（降级行为在后续节点实现）。
