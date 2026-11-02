# syntax=docker/dockerfile:1

# realm server image: the Go binary with the web UI embedded, on distroless.
#
#   docker build -t realm .
#   docker run -p 8080:8080 realm server --dev
#   docker run -p 8080:8080 -v ./realm.json:/etc/realm/realm.json:ro realm
#
# The build stages run on the build machine's platform and cross-compile for
# TARGETARCH, so multi-arch builds need no emulation.

FROM --platform=$BUILDPLATFORM node:24-alpine AS ui
WORKDIR /src/http/realm-ui
COPY http/realm-ui/package.json http/realm-ui/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY http/realm-ui/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=development
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/http/realm-ui/dist ./http/realm-ui/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -tags=ui \
      -ldflags "-s -w -X github.com/steviebps/realm/cmd.Version=${VERSION}" \
      -o /out/realm . \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="realm" \
      org.opencontainers.image.description="Feature flags and configuration with targeting, progressive rollout, and real-time sync" \
      org.opencontainers.image.source="https://github.com/steviebps/realm" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /out/realm /usr/local/bin/realm
# Writable data directory for the file and boltdb backends.
COPY --from=build --chown=nonroot:nonroot /out/data /var/lib/realm
VOLUME /var/lib/realm
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/realm"]
CMD ["server", "--config", "/etc/realm/realm.json"]
