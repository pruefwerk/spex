FROM golang:1.25.11-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/spex-probe-influxdb ./cmd/spex-probe-influxdb

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/spex-probe-influxdb /spex-probe-influxdb
USER nonroot:nonroot
ENTRYPOINT ["/spex-probe-influxdb"]
