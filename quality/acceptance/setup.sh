#!/usr/bin/env bash
# 验收环境：在本地基础设施（deployments/docker-compose.yml，项目名 blink_shop）上起应用层部署（deployments/app）。
# 专用的库 / 账号 / 桶 / 集合，凭据随机生成写进 deployments/app/app.env（不提交）；API 以 APP_ENV=production 运行。
# 用法：quality/acceptance/setup.sh [web 端口，默认 18088]；结束后 quality/acceptance/teardown.sh 全部清理。
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
PORT=${1:-18088}
DB=blink_shop_accept DBUSER=blink_accept BUCKET=blink-shop-accept MINIOUSER=blink-accept COLL=blink_shop_accept_images
ENVF="$ROOT/deployments/app/app.env"
rand() { openssl rand -hex 20; }

if lsof -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then echo "端口 $PORT 已被占用" >&2; exit 1; fi
for c in blink_shop-mysql-1 blink_shop-minio-1 blink_shop-milvus-1; do
  docker inspect -f '{{.State.Health.Status}}' "$c" | grep -q healthy || { echo "$c 不健康，先启动本地基础设施" >&2; exit 1; }
done

DBPW=$(rand) MINIOPW=$(rand) METRICS=$(rand)
docker exec -i blink_shop-mysql-1 sh -c 'mysql -uroot -p"$MYSQL_ROOT_PASSWORD"' 2>/dev/null <<SQL
DROP DATABASE IF EXISTS $DB; CREATE DATABASE $DB CHARACTER SET utf8mb4;
DROP USER IF EXISTS '$DBUSER'@'%'; CREATE USER '$DBUSER'@'%' IDENTIFIED BY '$DBPW';
GRANT ALL PRIVILEGES ON $DB.* TO '$DBUSER'@'%';
SQL
docker exec blink_shop-minio-1 sh -c "mc alias set accept http://127.0.0.1:9000 \"\$MINIO_ROOT_USER\" \"\$MINIO_ROOT_PASSWORD\" >/dev/null \
  && mc admin user add accept $MINIOUSER $MINIOPW >/dev/null && mc admin policy attach accept readwrite --user $MINIOUSER >/dev/null; mc alias rm accept >/dev/null"

umask 077
cat > "$ENVF" <<ENV
APP_ENV=production
MYSQL_DSN=$DBUSER:$DBPW@tcp(mysql:3306)/$DB?parseTime=true&charset=utf8mb4
RUN_MIGRATIONS=false
BOOTSTRAP_VECTOR_INDEX=false
CORS_ALLOWED_ORIGINS=http://127.0.0.1:$PORT
TRUSTED_PROXY_CIDRS=172.16.0.0/12
SHUTDOWN_DRAIN=5s
METRICS_TOKEN=$METRICS
MINIO_ENDPOINT=minio:9000
MINIO_BUCKET=$BUCKET
MINIO_ACCESS_KEY=$MINIOUSER
MINIO_SECRET_KEY=$MINIOPW
MILVUS_ADDR=milvus:19530
MILVUS_TOKEN=accept:$(rand)
MILVUS_IMAGE_COLLECTION=$COLL
RATE_LIMIT_IP_PER_MINUTE=6000
RATE_LIMIT_ACCOUNT_PER_MINUTE=6000
LOGIN_ATTEMPTS_PER_MINUTE=1000
ENV

export BLINK_WEB_PORT=$PORT BLINK_VERSION=accept
COMPOSE=(docker compose -f "$ROOT/deployments/app/docker-compose.yml" --env-file "$ENVF")
"${COMPOSE[@]}" build -q api
echo "== 迁移（/migrate -apply）"
docker run --rm --network blink_shop_default --env-file "$ENVF" --entrypoint /migrate blink-shop-api:accept -apply
echo "== 演示数据（cmd/seed 在生产模式拒绝运行，这里用开发模式写入同一个库）"
(cd "$ROOT/backend" && env -i PATH="$PATH" HOME="$HOME" MYSQL_DSN="$DBUSER:$DBPW@tcp(127.0.0.1:3307)/$DB?parseTime=true" go run ./cmd/seed | head -2)
"${COMPOSE[@]}" up -d --wait
echo "== 就绪：http://127.0.0.1:$PORT  $(curl -s http://127.0.0.1:$PORT/api/v1/ready)"
