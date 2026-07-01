# Mercator broker image.
#
# Mercator's Docker adapter drives the host Docker daemon through the `docker`
# CLI, so this image ships the static mercator binary alongside the Docker CLI.
# Run it with the host Docker socket mounted:
#
#   docker run --rm \
#     -e MERCATOR_ADDR=0.0.0.0:8080 \
#     -e MERCATOR_DOCKER_ARCH=amd64 \
#     -e MERCATOR_API_TOKEN=dev-token -e MERCATOR_AUTH_WORKSPACES=ws_1 \
#     -v /var/run/docker.sock:/var/run/docker.sock \
#     -p 8080:8080 mercator:local serve
#
# Mounting the Docker socket grants this container root-equivalent control of
# the host Docker daemon. That is fine for local evaluation on a machine you
# own; do not do it on an untrusted host.
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/mercator ./cmd/mercator

FROM docker:29-cli
COPY --from=build /out/mercator /usr/local/bin/mercator
RUN mkdir -p /data
WORKDIR /data
EXPOSE 8080
ENTRYPOINT ["mercator"]
CMD ["serve"]
