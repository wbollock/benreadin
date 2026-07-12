FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Only Go sources here; public/ is copied straight into the final stage, so
# frontend-only changes never trigger a recompile.
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/benreadin ./cmd/benreadin

# Pre-create the data dir so the named volume inherits nonroot ownership on
# first init (distroless nonroot is uid:gid 65532).
RUN mkdir -p /data


FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /

COPY --from=build /out/benreadin /benreadin
COPY public /public
COPY internal/db/schema.sql /internal/db/schema.sql
COPY --from=build --chown=65532:65532 /data /app/data

EXPOSE 3000
ENTRYPOINT ["/benreadin"]
