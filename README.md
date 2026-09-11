# lazywrap

Lazy-start HTTP/gRPC/TCP proxy and local process supervisor for development services.

`lazywrap` keeps local services stopped until they are needed. The first request to a configured route starts the target service, waits for it to become ready, proxies the request, and shuts the service down again after it has been idle for a configured period.

Written in Go.

## Why

Local development environments often require many services, but most of them do not need to run continuously.

`lazywrap` gives you one stable local endpoint while starting individual services only when traffic arrives.

```text
Client
  |
  v
localhost:3000
  |
  +-- /service/api/* ----> localhost:1980
  |
  +-- /service/web/* ----> localhost:1981
  |
  +-- /service/admin/* --> localhost:1982
```

Services remain stopped until requested.

## Features

* Lazy-start services on first request
* Reverse proxy HTTP traffic by path or hostname
* Proxy native gRPC over HTTP/2 cleartext (h2c)
* Proxy raw TCP traffic on per-service listener ports
* Automatic idle shutdown
* Per-service idle timeout
* Per-service environment variables
* Optional build command before launch
* Configurable working directory
* Optional custom stop command
* Automatic process termination when no stop command exists
* Readiness detection before proxying traffic
* Concurrent startup deduplication
* Child process stdout/stderr forwarding
* Per-service log prefixes
* Configurable log levels
* Graceful shutdown on `SIGINT` / `SIGTERM`
* Minimal dependencies

## Installation

```bash
go install github.com/YOUR_USERNAME/lazywrap/cmd/lazywrap@latest
```

Or build from source:

```bash
git clone https://github.com/and1truong/lazywrap.git
cd lazywrap
go build -o lazywrap ./cmd/lazywrap
```

## Releases

Pushing a semantic-version tag creates a GitHub release with checksums and archives for macOS, Linux, and Windows on AMD64 and ARM64:

```bash
git tag v0.1.0
git push origin v0.1.0
```

The release workflow runs the test suite and `go vet`, then GoReleaser publishes the generated artifacts to [GitHub Releases](https://github.com/and1truong/lazywrap/releases).

## Usage

```bash
lazywrap -c /path/to/config.yaml
```

Without `-c`, `lazywrap` uses:

```text
~/.config/lazywrap.yaml
```

## Configuration

```yaml
port: 3000
idle: 30m
startTimeout: 30s
stopTimeout: 10s
logLevel: info

apps:
  SERVICE_ID:
    pwd: /path/to/dir
    build: /command/to/build/it
    launch: /command/to/start/it
    stop: /optional/command/to/stop/it
    env:
      APP_ENV: development
      LOG_LEVEL: debug
    protocol: http # http (default), grpc, or tcp
    # Optional HTTP routing override. If both are omitted, host defaults to
    # SERVICE_ID.localhost.
    # path: /service/SERVICE_ID
    # host: custom.localhost
    # TCP services use listenPort instead of path or host:
    # listenPort: 11980
    port: 1980
    # Optional for gRPC services. Wait for the standard health service to
    # report SERVING instead of using TCP port readiness.
    # health:
    #   grpc: true
    idle: 5m # set to 0 to disable automatic idle shutdown
    includePrefix: false # path routing only
```

### Global options

| Option         | Description                                        | Default  |
| -------------- | -------------------------------------------------- | -------- |
| `port`         | Port `lazywrap` listens on                         | `3000`   |
| `idle`         | Default service idle timeout                       | `30m`    |
| `startTimeout` | Maximum time to wait for a service to become ready | `30s`    |
| `stopTimeout`  | Maximum time allowed for service shutdown          | `10s`    |
| `logLevel`     | `debug`, `info`, `warning`, or `error`             | `info`   |

### Service options

| Option          | Description                                                    |
| --------------- | -------------------------------------------------------------- |
| `pwd`           | Working directory for build, launch, and stop commands         |
| `build`         | Optional command executed before starting the service          |
| `launch`        | Command used to start the service                              |
| `stop`          | Optional command used to stop the service                      |
| `env`           | Environment variables for build, launch, and stop commands; app values override inherited variables |
| `protocol`      | Proxy protocol: `http` (default), `grpc`, or `tcp`               |
| `path`          | Optional public path prefix; mutually exclusive with `host`              |
| `host`          | Exact hostname; defaults to `<SERVICE_ID>.localhost` when `path` is omitted |
| `listenPort`    | Public local listener port; required for TCP services           |
| `port`          | Local port used by the service                                 |
| `idle`          | Overrides the global idle timeout; `0` disables idle shutdown  |
| `includePrefix` | Whether the configured path prefix is preserved; path routing only |
| `health.grpc`   | For gRPC, wait for the standard health service to report `SERVING` |

## Example

Configuration:

```yaml
port: 3000
idle: 30m

apps:
  api:
    pwd: ~/code/my-api
    launch: go run ./cmd/api
    env:
      APP_ENV: development
      LOG_LEVEL: debug
    path: /service/api
    port: 1980
    idle: 5m
    includePrefix: false
```

Start `lazywrap`:

```bash
lazywrap
```

Then request:

```bash
curl http://localhost:3000/service/api/hello
```

If `api` is stopped, `lazywrap`:

1. Starts the service.
2. Waits for `localhost:1980` to accept connections.
3. Proxies the request to:

```text
http://localhost:1980/hello
```

4. Keeps the service running while it receives traffic.
5. Stops it after five minutes without requests.

## Host routing

HTTP services default to host routing when neither `host` nor `path` is configured. The app ID becomes `<SERVICE_ID>.localhost`:

```yaml
apps:
  docs:
    pwd: ~/code/docs
    launch: npm run dev
    port: 1988
```

This exposes `docs` at `http://docs.localhost:3000`. You can also configure an exact hostname explicitly:

```yaml
apps:
  docs:
    pwd: ~/code/docs
    launch: npm run dev
    host: docs.localhost
    port: 1988
```

With wrapper port `3000`, `http://docs.localhost:3000/guide?q=1` is proxied to `http://127.0.0.1:1988/guide?q=1`. The path and query string are unchanged. Host matching is case-insensitive and ignores the wrapper port; it does not perform wildcard or suffix matching. `includePrefix` is not valid for host-routed services.

Backends receive `Host: 127.0.0.1:<service-port>` so they identify the local target consistently; the incoming hostname is retained in `X-Forwarded-Host`.

## gRPC proxying

Use `protocol: grpc` for native gRPC services:

```yaml
apps:
  greeter:
    pwd: ~/code/greeter
    launch: go run ./cmd/server
    protocol: grpc
    host: greeter.localhost
    port: 50051
    idle: 5m
    health:
      grpc: true
```

Connect a plaintext gRPC client to `greeter.localhost:3000`. lazywrap accepts HTTP/2 cleartext (h2c), starts the backend on the first RPC, waits for readiness, and proxies to `127.0.0.1:50051` without protobuf descriptors. Unary, client-streaming, server-streaming, bidirectional-streaming, metadata, deadlines, cancellation, status details, and trailers pass through transparently.

An open RPC or stream keeps the service active. The idle timer starts only after the final RPC closes. gRPC services use hostname routing and do not support `path`, `includePrefix`, or `listenPort`.

When `health.grpc` is enabled, the backend must implement the [standard gRPC health checking protocol](https://github.com/grpc/grpc/blob/master/doc/health-checking.md) and report `SERVING` for the overall server (`service: ""`). Without it, lazywrap considers the service ready when its TCP port accepts connections.

The initial implementation is local-development oriented: plaintext h2c only. TLS termination, TLS upstreams, and gRPC-Web translation are not currently supported.

## TCP proxying

Use `protocol: tcp` for PostgreSQL, Redis, MySQL, SSH, and other local TCP services:

```yaml
apps:
  postgres:
    pwd: ~/code/my-project
    launch: docker compose up postgres
    stop: docker compose stop postgres
    protocol: tcp
    listenPort: 15432
    port: 5432
    idle: 30m
```

Connect through the lazywrap listener:

```bash
psql postgres://user:password@127.0.0.1:15432/database
```

The first connection starts the service and waits for its target port to become ready. TCP bytes are then copied bidirectionally without protocol-specific processing. The service remains active while any connection is open; the idle timer begins only after the final connection closes. Long-lived connection pools therefore keep the service running.

TCP services require a unique `listenPort`. They do not support `path`, `host`, or `includePrefix` routing.

### Single-broker Kafka

A local single-broker Kafka instance works through the generic TCP proxy. Kafka must advertise the lazywrap listener, not its backend listener, because clients reconnect to the broker address returned in Kafka metadata.

Configure lazywrap:

```yaml
apps:
  kafka:
    pwd: ~/dev/kafka
    launch: bin/kafka-server-start.sh config/server.properties
    protocol: tcp
    listenPort: 19092
    port: 9092
    idle: 0
```

Configure the Kafka broker:

```properties
listeners=PLAINTEXT://127.0.0.1:9092
advertised.listeners=PLAINTEXT://127.0.0.1:19092
```

Configure clients:

```properties
bootstrap.servers=127.0.0.1:19092
```

Use `idle: 0` for Kafka. Producers and consumers commonly keep connections open and automatically reconnect; an idle shutdown would otherwise be ineffective or create a stop/reconnect/start loop.

This setup intentionally supports localhost and one broker. It does not parse Kafka frames or rewrite metadata. Multi-broker clusters require a separate proxied listener and advertised address for every broker.

To run the opt-in integration test against a local Kafka distribution:

```bash
KAFKA_HOME=/path/to/kafka go test -tags=integration ./integration
```

The test formats an isolated single-node KRaft data directory, starts Kafka lazily through lazywrap, and verifies topic administration plus a producer/consumer round trip using Kafka's CLI clients.

## Path forwarding

Given:

```yaml
path: /service/api
port: 1980
includePrefix: false
```

This request:

```text
GET http://localhost:3000/service/api/users
```

becomes:

```text
GET http://localhost:1980/users
```

With:

```yaml
includePrefix: true
```

the same request becomes:

```text
GET http://localhost:1980/service/api/users
```

HTTP methods, headers, query parameters, request bodies, response status, and response bodies are preserved.

## Service lifecycle

```text
stopped
   |
   v
building
   |
   v
starting
   |
   v
running
   |
   v
stopping
   |
   v
stopped
```

A failed build or startup transitions the service into a failure state and returns an error to the triggering request.

Concurrent requests do not cause duplicate service launches. Requests arriving while a service is starting wait for the same startup operation.

## Idle shutdown

Every successful request updates the service's activity time.

A service is eligible for shutdown when:

```text
time since last activity >= idle timeout
AND
active requests == 0
```

This prevents LazyWrap from stopping a service while it is still handling traffic.

## Process management

If `stop` is configured, LazyWrap executes it when the service needs to shut down.

Otherwise, LazyWrap terminates the process it started.

Graceful termination is attempted first. A process that does not exit within the shutdown timeout may be forcefully terminated.

## Logging

Example:

```text
INFO  [api] starting service
INFO  [api] stdout: listening on :1980
INFO  [api] service ready
DEBUG [api] GET /service/api/users -> http://localhost:1980/users
INFO  [api] idle timeout reached
INFO  [api] stopping service
```

Child process stdout and stderr are forwarded through LazyWrap with the service name attached.

Supported levels:

```text
debug
info
warning
error
```

## Graceful shutdown

When LazyWrap receives `SIGINT` or `SIGTERM`, it:

1. Stops accepting new work.
2. Gracefully shuts down the HTTP server.
3. Stops managed services.
4. Waits for cleanup.
5. Exits.

## Use cases

LazyWrap is useful for:

* local microservice development
* rarely used development tools
* AI/agent development environments
* local dashboards and admin services
* reducing CPU and memory use from idle services
* providing stable local URLs for dynamically started applications

## Status

Early development.

The initial goal is intentionally small:

> One process, one configuration file, one public port, and local services that wake on demand.

## License

MIT
