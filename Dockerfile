# The container image behind the EKS deployment in infra/terraform.
#
# The application is a single static binary — templates/ and webapp/dist are
# compiled into it by the //go:embed directives in messageServer.go and
# webapp.go — so there is nothing to install at runtime and no Node on the
# final image. A change to the React sources reaches a browser only after
# `npm run build` in webapp/ and a rebuild of this image, exactly as on a laptop.
#
# Two binaries are built. messageServer is the entrypoint; migrate is here so
# the schema can be applied once by a Job before the pods start, rather than by
# three pods racing each other at boot (see kubernetes.tf).

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies first, so editing Go sources does not re-download the module
# cache on every build.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# BUILDPLATFORM above plus TARGET* here means this cross-compiles: an arm64
# laptop can build the linux/amd64 image the node group runs without emulation.
ARG TARGETOS
ARG TARGETARCH

# CGO_ENABLED=0 is what makes the binaries static. It costs nothing here: the
# Postgres driver is pure Go and so is modernc.org/sqlite.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
        go build -trimpath -ldflags="-s -w" -o /out/messageServer ./cmd/messageServer && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
        go build -trimpath -ldflags="-s -w" -o /out/migrate ./migrate

# Alpine rather than a distroless base: it keeps a shell, which is what makes
# `kubectl exec` useful for the -pending and -authorize admin flows.
FROM alpine:3

RUN apk add --no-cache ca-certificates && \
    adduser -D -u 10001 app

COPY --from=build /out/messageServer /out/migrate /usr/local/bin/

USER app
EXPOSE 8080

# No .env file is baked in. LoadEnv treats a missing file as "the value comes
# from the environment", and DATABASE_URL arrives from a Kubernetes Secret.
ENTRYPOINT ["/usr/local/bin/messageServer"]
CMD ["-addr=:8080"]
