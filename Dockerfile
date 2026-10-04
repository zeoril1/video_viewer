# syntax=docker/dockerfile:1
#
# Один Dockerfile для всех микросервисов video_viewer: ARG SERVICE
# выбирает, какой бинарник собрать (gateway|catalog|stream|auth), а
# WITH_FFMPEG=1 включает ffmpeg в образ (нужен только stream-сервису).
#
# Пример:
#   docker build --build-arg SERVICE=gateway -t video-viewer-gateway .
#   docker build --build-arg SERVICE=stream --build-arg WITH_FFMPEG=1 -t video-viewer-stream .
#
ARG SERVICE=gateway
ARG WITH_FFMPEG=0

# ---------- Стадия сборки ----------
FROM golang:1.27-alpine AS build

# Не скачивать тулчейн автоматически — используем версию из образа
ENV GOTOOLCHAIN=local

ARG SERVICE

WORKDIR /src

# Сначала только манифесты — кэшируем слой зависимостей
COPY go.mod go.sum ./
RUN go mod download

# Исходники и сборка статического бинарника нужного сервиса
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${SERVICE}

# ---------- Стадия запуска ----------
FROM alpine:3.20

# ca-certificates — для HTTPS-трекеров и веб-сидов.
# ffmpeg — только для stream-сервиса (транскодинг HLS); остальные сервисы
# собираются без него (меньше и безопаснее образ).
ARG WITH_FFMPEG
RUN apk add --no-cache ca-certificates tzdata \
 && if [ "$WITH_FFMPEG" = "1" ]; then apk add --no-cache ffmpeg; fi

WORKDIR /app

COPY --from=build /out/app /app/app

# Статика gateway (в compose перекрывается volume).
COPY web /app/web

CMD ["/app/app"]
