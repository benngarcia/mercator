FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/mercator-conformance-probe ./cmd/mercator-conformance-probe
COPY internal/conformanceprobe ./internal/conformanceprobe
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/mercator-conformance-probe ./cmd/mercator-conformance-probe

FROM alpine:3.22 AS certificates

FROM scratch
COPY --from=certificates /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/mercator-conformance-probe /mercator-conformance-probe
ENTRYPOINT ["/mercator-conformance-probe"]
