# Two images, chosen with --target; compose.yaml builds both.
#   api: the HTTP server plus the one-shot commands (migrate, policy, bootstrap-admin)
#   web: the built frontend behind nginx, which also proxies /api/ to the server
# Neither image holds configuration or credentials; they come from the environment.

FROM golang:1.27.3-alpine3.24 AS api-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

FROM alpine:3.24 AS api
# The media folder exists in the image so a new named volume inherits its owner.
RUN adduser -S -D -H -u 10001 webee && install -d -o webee -m 750 /data /data/media
COPY --from=api-build /out/ /app/
USER 10001
EXPOSE 8080
CMD ["/app/server"]

FROM node:24.22.1-alpine3.24 AS web-build
WORKDIR /src
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM nginxinc/nginx-unprivileged:1.31.7-alpine3.24 AS web
COPY deploy/nginx/default.conf /etc/nginx/conf.d/default.conf
COPY deploy/nginx/security-headers.conf /etc/nginx/security-headers.conf
COPY --from=web-build /src/dist/ /usr/share/nginx/html/
