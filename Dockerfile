ARG BUILDPLATFORM

FROM --platform=$BUILDPLATFORM oven/bun:1.3.5-alpine AS hapi-web
ARG HAPI_REPOSITORY=https://github.com/18345174/hapi.git
ARG HAPI_REF=main
RUN apk add --no-cache git
WORKDIR /src/hapi
RUN git init \
    && git remote add origin "$HAPI_REPOSITORY" \
    && git fetch --depth=1 origin "$HAPI_REF" \
    && git checkout --detach FETCH_HEAD
RUN bun install --frozen-lockfile
RUN VITE_BASE_URL=/hapi/ bun run build:web

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/echoear-cloud ./cmd/api

FROM alpine:3.22
RUN apk add --no-cache ca-certificates postgresql-client tzdata
WORKDIR /app
COPY --from=build /out/echoear-cloud /app/echoear-cloud
COPY --from=hapi-web /src/hapi/web/dist /app/hapi-web
COPY migrations /app/migrations
COPY scripts/docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod 0755 /app/docker-entrypoint.sh
EXPOSE 8080
ENTRYPOINT ["/app/docker-entrypoint.sh"]
