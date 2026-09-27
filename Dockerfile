# syntax=docker/dockerfile:1

# ---- frontend ----
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- backend ----
FROM golang:1.26-alpine AS server
WORKDIR /server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /graphly .

# ---- runtime: one static binary serving the API, WebSockets and the built SPA ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=server /graphly /graphly
COPY --from=web /web/dist /web
ENV ADDR=:8080 STATIC_DIR=/web
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/graphly"]
