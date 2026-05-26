FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /bin/benreadin ./cmd/benreadin


FROM alpine:3.21

RUN addgroup -S benreadin && adduser -S -G benreadin benreadin

WORKDIR /app
COPY --from=build /bin/benreadin .
COPY public/ public/

RUN mkdir -p data && chown -R benreadin:benreadin /app

USER benreadin

EXPOSE 3000
ENTRYPOINT ["/app/benreadin"]
