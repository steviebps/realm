# syntax=docker/dockerfile:1

# realm server image: the Go binary with the web UI embedded, on distroless.
#
#   docker build -t realm .
#   docker run -p 8080:8080 realm server --dev
#   docker run -p 8080:8080 -v ./realm.json:/etc/realm/realm.json:ro realm
#
# To require authentication without setting up sign-in, declare an admin key
# in the config ("auth": {"adminKey": "${REALM_ADMIN_KEY}"}), pass it in with
# -e REALM_ADMIN_KEY, and make API keys through the running server:
#
#   REALM_TOKEN=$REALM_ADMIN_KEY realm client keys create --name sdk --access read
#
# The build stages run on the build machine's platform and cross-compile for
# TARGETARCH, so multi-arch builds need no emulation.

FROM --platform=$BUILDPLATFORM node:24-alpine AS ui
WORKDIR /src/http/realm-ui
COPY http/realm-ui/package.json http/realm-ui/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY http/realm-ui/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/http/realm-ui/dist ./http/realm-ui/dist
# Declared late so the layers above are shared across architectures and versions.
ARG TARGETOS TARGETARCH
ARG VERSION=development
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -tags=ui \
      -ldflags "-s -w -X github.com/steviebps/realm/cmd.Version=${VERSION}" \
      -o /out/realm .

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="realm" \
      org.opencontainers.image.description="Feature flags and configuration with targeting, progressive rollout, and real-time sync" \
      org.opencontainers.image.source="https://github.com/steviebps/realm" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /out/realm /usr/local/bin/realm
# Writable data directory for the file and boltdb backends; WORKDIR creates it
# owned by the image's nonroot user.
USER nonroot:nonroot
WORKDIR /var/lib/realm
VOLUME /var/lib/realm
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/realm"]
# Reads /etc/realm/realm.json, one of realm's default config locations.
CMD ["server"]
