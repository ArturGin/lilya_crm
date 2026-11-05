# ---- web build ----
FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- go build ----
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/lilya ./cmd/lilya

# ---- runtime ----
FROM alpine:3.22
RUN apk add --no-cache ffmpeg ca-certificates tzdata \
 && adduser -D -u 10001 app \
 && mkdir -p /data && chown app:app /data
COPY --from=build /out/lilya /usr/local/bin/lilya
USER app
ENV DATA_DIR=/data LISTEN_ADDR=:8080
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["lilya"]
CMD ["serve"]
