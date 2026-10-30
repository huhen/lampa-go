# syntax=docker/dockerfile:1

# Stage 1: build the static server binary. The sqlite driver is pure Go
# (modernc), so CGO stays off and the binary is fully static.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/lampa-go ./cmd/lampa-go
# Runtime tree owned by the distroless nonroot user (uid/gid 65532): a fresh
# named volume mounted at /var/lib/lampa-go inherits the owner from the image.
RUN mkdir -p /out/rootfs/var/lib/lampa-go/web && chown -R 65532:65532 /out/rootfs/var

# Stage 2: minimal static runtime.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/lampa-go /lampa-go
COPY --from=build /out/rootfs/ /
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/lampa-go", "-config", "/etc/lampa-go/config.yaml"]
