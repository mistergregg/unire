# Unire controller image. The sandbox image is built from docker/sandbox.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/unire ./cmd/unire

FROM alpine:3.24
# The controller starts sandboxes by talking to the host's Docker through the
# mounted socket, using the docker CLI.
RUN apk add --no-cache docker-cli ca-certificates
COPY --from=build /out/unire /usr/local/bin/unire

# Where the database and master key live. Mount a volume here.
VOLUME /data
EXPOSE 8080
# Sessions use this sandbox image unless changed in Settings.
ENV UNIRE_SANDBOX_IMAGE=ghcr.io/mistergregg/unire-sandbox:latest

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/api/status || exit 1

ENTRYPOINT ["unire"]
CMD ["serve", "--addr", "0.0.0.0:8080", "--data", "/data"]
