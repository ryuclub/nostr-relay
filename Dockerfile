FROM golang:1.24.11-alpine AS builder

# 基础编译环境
WORKDIR /app

# 1. 显式拉取 eventstore 包
RUN go mod init my-relay
RUN go get github.com/fiatjaf/khatru@latest
RUN go get github.com/fiatjaf/eventstore/postgresql@latest
RUN go get github.com/nbd-wtf/go-nostr@latest

COPY . .
RUN go mod tidy

# 2. 编译：禁用 CGO (Postgres 不需要 CGO，除非使用特定的 libpq 绑定)
RUN CGO_ENABLED=0 GOOS=linux go build -o relay main.go

FROM alpine:latest
RUN apk add --no-cache ca-certificates
WORKDIR /root/
COPY --from=builder /app/relay .

EXPOSE 8383
CMD ["./relay"]