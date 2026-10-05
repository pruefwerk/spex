# HTTP integration bundle

Use this bundle for HTTP commands and JSON assertions. It contains no project-specific endpoints or migration rules. The probe uses Go's standard library and requires Go 1.23 or later.

Build the image from this directory:

```sh
docker build -t spex-http-probe:v0.1.0 .
```

Reference this directory as a local `http` bundle, version `0.1.0`. Spex releases also provide `spex-http-bundle_<release-version>.tar.gz` and its SHA-256 checksum. Keep the selected release pinned; the bundle contract version does not identify the source revision.

The bundle exposes `http.postJson`, `http.requestJson`, and `http.authenticatedRequestJson`. It reads the endpoint from the binding and optional bearer credentials from `credentials.token`. It preserves the operation-file and result-file protocol.

GET requests poll until assertions pass. POST, PUT, and DELETE send once unless the scenario explicitly sets `retryWrites: true`. Enable write retries only when the operation supports retransmission. The probe resolves `bodyFieldsFrom` and `expectedBodyFrom` through GET requests once, before any retries, so retries retain the original input.

Assert the HTTP status, complete JSON body, JSON-pointer fields, or array length. Use `contentType` and `headers` for non-JSON payloads. Failed assertions produce a failed result envelope; they do not make a write safe to repeat.

Run compatibility tests:

```sh
cd probe
go test -race ./...
go vet ./...
```
