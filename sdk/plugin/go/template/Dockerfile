FROM golang:1.26-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY api/proto/plugin/v1 ./api/proto/plugin/v1
COPY sdk/plugin/go ./sdk/plugin/go

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/plugin ./sdk/plugin/go/template

FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/plugin /plugin
EXPOSE 9000
USER 65532:65532
ENTRYPOINT ["/plugin"]
