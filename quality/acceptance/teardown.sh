#!/usr/bin/env bash
# 清理 setup.sh 建的一切：应用层容器和卷、库和账号、MinIO 账号和桶、Milvus 临时集合、app.env、验收镜像。
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
ENVF="$ROOT/deployments/app/app.env"
DB=blink_shop_accept DBUSER=blink_accept BUCKET=blink-shop-accept MINIOUSER=blink-accept COLL=blink_shop_accept_images
[ -f "$ENVF" ] && docker compose -f "$ROOT/deployments/app/docker-compose.yml" --env-file "$ENVF" down -v --remove-orphans
docker exec -i blink_shop-mysql-1 sh -c 'mysql -uroot -p"$MYSQL_ROOT_PASSWORD"' 2>/dev/null <<SQL
DROP DATABASE IF EXISTS $DB; DROP USER IF EXISTS '$DBUSER'@'%';
SQL
docker exec blink_shop-minio-1 sh -c "mc alias set accept http://127.0.0.1:9000 \"\$MINIO_ROOT_USER\" \"\$MINIO_ROOT_PASSWORD\" >/dev/null; \
  mc rb --force accept/$BUCKET >/dev/null 2>&1; mc admin user remove accept $MINIOUSER >/dev/null 2>&1; mc alias rm accept >/dev/null"
curl -s -X POST http://127.0.0.1:19530/v2/vectordb/collections/drop -H 'Content-Type: application/json' -d "{\"collectionName\":\"$COLL\"}" >/dev/null
rm -f "$ENVF"
docker rmi -f blink-shop-api:accept >/dev/null 2>&1
echo "== 清理完成；Milvus 集合：$(curl -s -X POST http://127.0.0.1:19530/v2/vectordb/collections/list -H 'Content-Type: application/json' -d '{}')"
