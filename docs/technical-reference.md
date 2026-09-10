# Technical reference

Setup, configuration, API behavior, validation, and container publication for the OneMind Services Speedtest API. See the [project overview](../README.md) for its purpose and capabilities.

## Run locally

Use Go 1.27.1 or newer. Go's automatic toolchain selection can download the version specified in `go.mod`.

```sh
go run ./cmd/speedtest-api
```

The API listens on HTTP `:8080` and WebRTC UDP `:8081`, identifies as `local`, and allows the UI origins `http://localhost:3000` and `http://127.0.0.1:3000`. Start the sibling UI with `npm ci && npm run dev`, then open `http://localhost:3000`.

Optional configuration lives in environment variables. `.env` files are **not** loaded automatically. To use the supplied local example:

```sh
set -a
. ./.env.example
set +a
go run ./cmd/speedtest-api
```

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `LOG_LEVEL` | `info` | JSON log threshold: `debug`, `info`, `warn`, `error` |
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `WEBRTC_LISTEN_ADDR` | `:8081` | Single IPv4 UDP port shared by WebRTC sessions; empty disables packet loss |
| `WEBRTC_PUBLIC_IP` | empty | Optional public IPv4 address to advertise behind 1:1 NAT; external UDP port must match the listening port |
| `SPEEDTEST_ENV` | `development` | Set `production` to enforce non-local region identity, HTTPS origins and a private metrics listener |
| `METRICS_LISTEN_ADDR` | empty | Optional private IP:port for metrics; required in production (for example `127.0.0.1:9090`) |
| `MAX_TRANSFERS_PER_CLIENT` | `16` | Active HTTP transfers per resolved client IP; 1–1024 |
| `MAX_WEBRTC_PER_CLIENT` | `16` | Active packet-loss sessions per resolved client IP; 1–1024 |
| `MAX_WEBRTC_SESSIONS` | `64` | Maximum WebRTC sessions per process; 1–1024 |
| `REGION_ID` | `local` | Stable lowercase ID, matching the UI registry; 1–63 letters/digits/hyphens |
| `REGION_NAME` | `Local development` | Human-readable server name |
| `ALLOWED_ORIGINS` | Both local UI origins above | Comma-separated exact origins, without paths or trailing slashes; no wildcard |
| `MAX_DOWNLOAD_BYTES` | `67108864` | Maximum payload per download; 1 byte–1 GiB |
| `MAX_UPLOAD_BYTES` | `67108864` | Maximum payload per upload, including chunked bodies; 1 byte–1 GiB |
| `MAX_CONCURRENT_TRANSFERS` | `64` | Concurrent upload/download requests per process; 1–1024 |
| `ASN_DATABASE_PATH` | empty | Optional read-only MaxMind-compatible ASN MMDB file; empty disables ASN lookup |
| `TRUSTED_PROXY_CIDRS` | empty | Comma-separated trusted proxy CIDRs used only to resolve network identity from `X-Forwarded-For`; default routes are rejected |
| `REQUEST_TIMEOUT` | `30s` | HTTP read/write deadline; 1 second–2 minutes |

One test normally uses four parallel transfers. This concurrency limit is per request, not per user; simultaneous tests share the same server capacity. Requests exceeding global capacity receive `503`; the per-client bound returns `429`. Both include `Retry-After: 5`. Client counters exist only while transfers are active. Shared NAT addresses share this bound. Zero-byte probes bypass transfer slots so loaded latency remains measurable. Configuration errors fail startup.

## HTTP protocol, version 1

| Endpoint | Behavior |
| --- | --- |
| `GET /v1/info` | Region identity, protocol version, payload/concurrency limits and `capabilities.packetLoss`. Returns 503 while draining or at transfer capacity. |
| `GET /v1/network` | Observed client address and family, region identity, API-hop HTTP protocol, and optional ASN/network organization. Missing lookup data has an explicit status. |
| `GET /__down?bytes=N` | Exactly N uncompressed random bytes. Missing `bytes` means zero; malformed, fractional, negative and duplicate values return 400, excessive sizes return 413. |
| `POST /__up` | Streams the entire body to discard before returning `{"bytes":N,"regionId":"local"}`. Oversized bodies return 413; incomplete bodies return 400. Encoded request bodies return 415. |
| `POST /v1/packet-loss` | JSON WebRTC offer → JSON answer. Requires an allowed browser Origin; SDP body capped at 32 KiB. 501 when disabled, 503 when session capacity is reached. |
| `GET /healthz` | Process liveness |
| `GET /readyz` | Readiness; 503 while draining |
| `GET /metrics` | Available only on the private listener when `METRICS_LISTEN_ADDR` is set; Prometheus counters for accepted/rejected transfers and payload bytes, plus active transfer count |

`OPTIONS` supports the applicable method and the `Content-Type` request header. Methods are checked explicitly; `HEAD` is not a measurement. Every response includes `X-Speedtest-Region` and `Cache-Control: no-store, no-transform`. Allowed browser origins receive CORS and `Timing-Allow-Origin` headers. Downloads also include `Content-Length`, `Server-Timing: processing;dur=...`, and `X-Accel-Buffering: no`.

Downloads reuse a 1 MiB random buffer; memory does not grow with requested payload size. Uploads are streamed rather than buffered. Server counters include payload bytes written/read, including partial failed transfers; they are not a count of completed browser results. JSON logs include startup/shutdown events and HTTP requests with region, request ID, method, route, status, outcome, duration, bytes received and bytes sent. Logging counts streamed bytes without buffering payloads. `X-Request-ID` correlates an HTTP response with its log entry. Client addresses, query strings, request headers, payloads and WebRTC SDP are excluded; unknown paths are logged as `unmatched`. CORS rejections and interrupted responses are warnings; unavailable responses are errors. Successful health/metrics requests and CORS preflights are debug-only. `LOG_LEVEL` selects `debug`, `info` (default), `warn` or `error`. SIGTERM/SIGINT mark the server unready and wait for active requests to finish within the request timeout plus one second.

## Connecting regions

Give each regional API its own HTTPS origin. Set its `REGION_ID` and `ALLOWED_ORIGINS`, then add the same identity and endpoint origin to the UI's `public/config.json`. The UI will be hosted on Vercel and contacts those origins directly. Set `ALLOWED_ORIGINS` to its exact production HTTPS origin, plus any explicitly enabled preview origins. Measurement traffic does not pass through Vercel. Updating the UI’s bundled registry on Vercel requires a new deployment. A region identity mismatch is treated as unavailable.

When deployment work is added, the regional reverse proxy must preserve streaming, disable response/request buffering and compression, disable caching/CDN acceleration, allow the configured body sizes, and preserve CORS/timing/identity headers. Otherwise the test measures the proxy or fails protocol validation. TLS terminates at that regional proxy. The UI accepts plain HTTP only for localhost development on an HTTP page.

CORS controls browser access; it is not client authentication. Origin-free CLI requests are accepted for HTTP measurements; WebRTC offers require an allowed Origin. Production mode requires metrics on a separate private listener. Public exposure still needs an ingress request/connection abuse policy, network isolation and appropriate egress bandwidth; CORS and per-client transfer bounds do not prevent distributed abuse. See the [production checklist](production.md).

## Validate

```sh
go test -race ./...
go vet ./...
go build ./cmd/speedtest-api
```

The API tests cover exact sizes, malformed input, upload EOF/receipts, chunked limits, interruption, concurrency, CORS, draining and an actual HTTP upload. The UI's Playwright suite additionally builds this repository and starts two real regional processes behind controlled latency proxies; see the [Speedtest UI documentation](https://github.com/Onemind-Services-LLC/speedtest-ui).

This server and the companion browser engine implement the HTTP protocol documented above. Measurement traffic remains between the browser and the selected regional server.

## UDP packet-loss protocol

The server uses Pion WebRTC with ICE-lite on a shared IPv4 UDP socket. It never initiates ICE connectivity checks to addresses supplied in an offer. A browser creates exactly one unordered data channel named `packet-loss-v1`, with `maxRetransmits: 0`. Each message is 64 bytes, beginning with a big-endian uint32 sequence in 0–999. Up to 1,000 binary messages are echoed without alteration. Invalid channel settings, message sizes, sequence bounds or extra channels close the peer.

Sessions expire after 20 seconds and close on connection failure or data-channel closure. The process limits active peers and allows up to `MAX_WEBRTC_PER_CLIENT` sessions per resolved client IP (16 by default); IP counters exist in memory only while sessions are active. This is a conservative abuse bound, not user authentication. A shared NAT concentrates clients behind one IP. Configure only actual proxy CIDRs in `TRUSTED_PROXY_CIDRS`; the same validated forwarding chain is used for network identity, HTTP quotas and WebRTC quotas. The application does not trust arbitrary `X-Forwarded-For` headers.

The HTTPS signaling request and UDP packets **must reach the same process**. A Kubernetes HTTP load balancer and an independently balanced UDP Service do not establish that affinity. Use a per-instance advertised UDP address/port or an affinity-aware routing design when deployment work begins. `WEBRTC_PUBLIC_IP` supports same-port 1:1 IPv4 NAT; it does not implement TURN, port rewriting, or shared UDP balancing. Browser ICE can connect directly to a publicly reachable ICE-lite server without an external STUN service. If UDP is blocked, the UI preserves HTTP results and marks packet loss unavailable.

## Capacity boundary

This source has bounded transfers and UDP sessions, graceful shutdown, and integration tests. It has not been validated for millions of simultaneous tests. Public deployment still needs measured capacity targets, regional sizing, telemetry, an ingress abuse policy and a validated UDP affinity design. Kubernetes manifests are intentionally deferred.

## Docker image and GitHub Actions

`API CI and image` runs Go formatting, module verification, vet and race tests. It uses the pinned `container-build.yml`, `container-scan.yml` and `container-publish.yml` workflows from `Onemind-Services-LLC/actions`. The built OCI artifact is verified by digest, smoke-tested as a non-root process with a read-only filesystem, and scanned before publication. Publication consumes that same artifact without rebuilding, includes provenance and an SBOM, and signs and verifies the image digest.

Successful protected pushes to `master` publish `registry.onemindservices.com/speedtest/api:master` and a full `sha-<commit>` tag. Protected version tags (`v1.2.3`) publish the corresponding version. The shared builder currently produces Linux AMD64 images; the Dockerfile also supports ARM64 cross-compilation. Pull requests, unprotected pushes and manual runs build and smoke-test locally without receiving registry credentials or publishing.

The registry must have a `speedtest` project. Provide the organization’s `DOCKER_USERNAME` and `DOCKER_PASSWORD` secrets to this repository with permission to push `speedtest/api`. The workflow uses the organization’s `ci-test` and `ci-build` runners; pull requests from forks use GitHub-hosted runners. Kubernetes packaging remains deferred.

The image is built from a pinned Go base and contains only the static application binary, running as UID/GID 65532. The Docker build context allows only the Go build inputs, excluding local environment files, credentials and development artifacts.

```sh
docker build -t speedtest-api:local .
python3 tests/container_smoke.py speedtest-api:local
docker run --rm -p 8080:8080 -p 8081:8081/udp speedtest-api:local
```

HTTP health checks are available at `/healthz` and `/readyz`. For a public deployment, configure the regional identity, exact UI origin, HTTPS termination and the UDP routing contract described above. Publishing an image does not deploy it to Kubernetes.

## Network identity and address-family diagnostics

`/v1/network` returns the address observed at this regional API, not an inferred location. It uses the socket peer unless that peer belongs to `TRUSTED_PROXY_CIDRS`. With trusted proxies it walks `X-Forwarded-For` from right to left and stops at the first untrusted address. Malformed chains fall back to the socket peer. Configure only the actual ingress/proxy ranges; ensure the ingress appends the peer address or replaces untrusted forwarded headers. This setting does not change the existing conservative UDP session limit by socket source IP.

To resolve ASN and network organization, obtain an ASN MMDB under its provider's license, mount it read-only, and set `ASN_DATABASE_PATH=/data/asn.mmdb`. The process opens it at startup and closes it after draining. An unreadable or invalid configured database fails startup. Replace the database and restart the process for updates. No client address is sent to an external lookup service. Private/local addresses, missing records, disabled lookup and lookup errors have distinct statuses. A VPN or proxy may identify a hosting network rather than the customer's ISP.

For IPv4 and IPv6 diagnostics, configure separate IPv4-only and IPv6-only HTTPS origins for the same region in the UI (`ipv4Url`, `ipv6Url`). The browser checks the returned address family and region identity before reporting a warmed median RTT. If an ingress forwards both families over IPv4, trusted proxy configuration must preserve the original address for this check. The existing UDP listener remains IPv4. The browser's Resource Timing protocol describes the browser-to-ingress hop; `serverProtocol` describes the ingress-to-API hop and can differ.

UDP measurements run during downloads, during uploads and once without transfer load. Each is a separate bounded session using the existing protocol. The browser requires load to remain active for the entire loaded measurement; otherwise it reports unavailable instead of inventing a loss result.
