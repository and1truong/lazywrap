# local-apps

Lazy-start reverse proxy and local process supervisor for development services.

`local-apps` keeps local services stopped until they are needed. The first request to a configured route starts the target service, waits for it to become ready, proxies the request, and shuts the service down again after it has been idle for a configured period.

Written in Go.

## Why

Local development environments often require many services, but most of them do not need to run continuously.

`local-apps` gives you one stable local endpoint while starting individual services only when traffic arrives.

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
* Reverse proxy HTTP traffic
* Automatic idle shutdown
* Per-service idle timeout
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
go install github.com/YOUR_USERNAME/lazywrap/cmd/local-apps@latest
```

Or build from source:

```bash
git clone https://github.com/YOUR_USERNAME/lazywrap.git
cd lazywrap
go build -o local-apps ./cmd/local-apps
```

## Usage

```bash
local-apps -c /path/to/config.yaml
```

Without `-c`, `local-apps` uses:

```text
~/.config/local-apps.yaml
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
    path: /service/SERVICE_ID
    port: 1980
    idle: 5m
    includePrefix: false
```

### Global options

| Option         | Description                                        | Default  |
| -------------- | -------------------------------------------------- | -------- |
| `port`         | Port `local-apps` listens on                       | `3000`   |
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
| `path`          | Public route exposed by LazyWrap                               |
| `port`          | Local port used by the service                                 |
| `idle`          | Overrides the global idle timeout                              |
| `includePrefix` | Whether the configured route prefix is preserved when proxying |

## Example

Configuration:

```yaml
port: 3000
idle: 30m

apps:
  api:
    pwd: ~/code/my-api
    launch: go run ./cmd/api
    path: /service/api
    port: 1980
    idle: 5m
    includePrefix: false
```

Start `local-apps`:

```bash
local-apps
```

Then request:

```bash
curl http://localhost:3000/service/api/hello
```

If `api` is stopped, `local-apps`:

1. Starts the service.
2. Waits for `localhost:1980` to accept connections.
3. Proxies the request to:

```text
http://localhost:1980/hello
```

4. Keeps the service running while it receives traffic.
5. Stops it after five minutes without requests.

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

TBD
