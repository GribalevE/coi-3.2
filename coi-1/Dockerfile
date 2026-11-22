# Dockerfile
# Образ собирает сервис и оба файла датасета из SQL-дампа ЕРН.

FROM golang:1.26-bookworm AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Датасеты готовятся один раз на этапе сборки: в рантайме SQL-дамп не нужен.
RUN go run ./cmd/seed -in data/ern_full.sql -persons /out/dataset.json -dltos /out/dltos.json

RUN CGO_ENABLED=0 go build -trimpath -o /out/coi ./cmd/server

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates wget && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /out/coi /app/coi
COPY --from=builder /out/dataset.json /app/data/dataset.json
COPY --from=builder /out/dltos.json /app/data/dltos.json

RUN mkdir -p /app/var \
    && useradd --system --uid 10001 coi \
    && chown -R coi:coi /app

ENV COI_DATA=/app/data/dataset.json
ENV COI_DLTOS=/app/data/dltos.json
ENV COI_DB=/app/var/coi.db
ENV COI_ADDR=:8080
ENV COI_ADMIN_USER=admin
ENV COI_ADMIN_PASSWORD=123

EXPOSE 8080
USER coi

ENTRYPOINT ["/app/coi"]
