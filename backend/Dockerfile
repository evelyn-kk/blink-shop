# Blink Shop API 生产镜像：静态编译，运行在 distroless（无 shell、非 root）。迁移工具 migrate 一起打进镜像，发布时先跑：
#   docker run --rm -e MYSQL_DSN=... blink-shop-api /migrate -apply
# 基础镜像固定到多架构 digest（更新方法见 deployments/README.md“镜像锁定”）。
FROM golang:1.26.9-bookworm@sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -ldflags "-s -w" -o /out/api ./cmd/api && go build -ldflags "-s -w" -o /out/migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/api /api
COPY --from=build /out/migrate /migrate
# 头像目录要可写：挂载卷到 /data（distroless 的 nonroot 用户 uid 65532）
ENV APP_ENV=production API_ADDR=:8080 AVATAR_UPLOAD_DIR=/data/avatar
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/api"]
