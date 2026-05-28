FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/benreadin ./cmd/benreadin


FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/benreadin /benreadin
COPY public /public
COPY internal/db/schema.sql /internal/db/schema.sql

EXPOSE 3000
ENTRYPOINT ["/benreadin"]
