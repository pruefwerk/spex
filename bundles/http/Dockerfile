ARG BUILDER_IMAGE=golang:1.23-bookworm
ARG BASE_IMAGE=debian:bookworm-slim
FROM ${BUILDER_IMAGE} AS build
WORKDIR /src
COPY probe/ ./
RUN CGO_ENABLED=0 go test ./... \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/http-probe .
FROM ${BASE_IMAGE}
RUN useradd --system --no-create-home --uid 65532 spex
COPY --from=build /out/http-probe /usr/local/bin/http-probe
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/http-probe"]
