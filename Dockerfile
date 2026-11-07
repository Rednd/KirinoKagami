# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/kirinokagami ./cmd/kirinokagami

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 10001 kirino \
    && mkdir -p /app/data && chown kirino /app/data
WORKDIR /app
COPY --from=build /out/kirinokagami /usr/local/bin/kirinokagami
USER kirino
EXPOSE 62011
VOLUME ["/app/data"]
ENV KIRINO_CONFIG=/app/config.toml
ENTRYPOINT ["kirinokagami"]
