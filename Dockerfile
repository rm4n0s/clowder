# The image of Clowder (SPEC D40): a static, cgo-free binary on a distroless
# base, running as the user "nonroot". The data directory is /data (a volume).

# ---- build ----
FROM golang:1.27 AS build
WORKDIR /src
# The modules first, so that this layer is cached until go.mod or go.sum change.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/clowder ./cmd/clowder
# The distroless image has no shell: make the empty data directory here, and
# copy it with its owner (65532 is "nonroot").
RUN mkdir /out/data

# ---- run ----
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/clowder /clowder
COPY --from=build --chown=65532:65532 /out/data /data
ENV CLOWDER_DATA=/data CLOWDER_ADDR=:8080
VOLUME /data
EXPOSE 8080 2222
USER nonroot:nonroot
# The image has no shell and no curl: the binary asks its own GET /healthz.
HEALTHCHECK --interval=10s --timeout=5s --start-period=20s --retries=3 CMD ["/clowder", "healthcheck"]
ENTRYPOINT ["/clowder"]
CMD ["serve"]
