FROM golang:1.25.11-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/spex-demo-stack ./cmd/spex-demo-stack

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/spex-demo-stack /spex-demo-stack
USER nonroot:nonroot
ENTRYPOINT ["/spex-demo-stack"]
