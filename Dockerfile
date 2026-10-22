ARG GO_VERSION=1.27.2
ARG GOOSE_VERSION=v3.29.0
ARG ALPINE_VERSION=3.22

FROM golang:${GO_VERSION} AS deps
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

FROM deps AS source
COPY . .

# Run by `docker compose run test`; never shipped. The debian base has gcc, which -race needs.
FROM source AS test
CMD ["go", "test", "-p", "1", "-race", "-count=1", "./..."]

FROM source AS build
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM golang:${GO_VERSION} AS goose
ARG GOOSE_VERSION
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/pressly/goose/v3/cmd/goose@${GOOSE_VERSION}

FROM alpine:${ALPINE_VERSION} AS runtime
RUN addgroup -S app && adduser -S -G app app
COPY --from=build /out/api /usr/local/bin/api
COPY --from=goose /out/goose /usr/local/bin/goose
COPY db/migrations /migrations
USER app
EXPOSE 8060
ENTRYPOINT ["api"]