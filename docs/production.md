# Production preparation

Version 0.1.1 is protocol-compatible with Speedtest UI 0.1.1 and earlier 0.1.x clients. The application version is returned by `/v1/info`; the wire protocol remains version 1. Publishing a release does not deploy a regional server.

## Deployment requirements

Set `SPEEDTEST_ENV=production`, a unique non-local `REGION_ID`, the regional display name, and the exact HTTPS UI origins in `ALLOWED_ORIGINS`. Set `METRICS_LISTEN_ADDR` to an explicit private or loopback IP and port. This removes `/metrics` from the public listener. Keep this management listener private using host/container network policy; an RFC1918 bind alone cannot prevent a proxy from exposing it.

For a direct TCP LoadBalancer, enable the API's TLS listener with `TLS_LISTEN_ADDR=:8443`, `TLS_CERT_FILE`, `TLS_KEY_FILE` and the exact `PUBLIC_ORIGIN`. Mount a renewable certificate Secret read-only; the API checks for a replacement every 30 seconds and retains the last valid pair if rotation is incomplete. The public listener requires matching SNI and Host, supports TLS 1.2 or newer, and uses HTTP/1.1 so measurement streams have independent TCP flow control. Keep the health listener on port 8080 private.

If the load balancer sends PROXY protocol, set `PROXY_PROTOCOL_TRUSTED_CIDRS` to its actual backend source ranges and restrict the public pod ports to those sources with NetworkPolicy. These listeners require a PROXY header from trusted peers and reject all other peers. Leave `TRUSTED_PROXY_CIDRS` empty on a direct deployment: it controls HTTP forwarding headers, which clients can forge. HTTP and UDP Services may share a cloud load balancer, but only the TCP Service should enable PROXY protocol.

Set `HTTP_REDIRECT_ADDR=:8082` to expose a separate HTTP-to-HTTPS redirect on public port 80. `BROWSER_REDIRECT_URL` optionally redirects top-level GET navigations to the UI without redirecting fetch/XHR, uploads or preflights. Public listeners set HSTS on HTTPS, prevent caching and MIME sniffing, hide metrics, limit each client to 200 requests/second with burst 400, and cap active requests at 128 per process. The client-rate table is bounded to 10,000 entries and expires inactive entries after one minute; new clients receive 429 while the table is full. Existing transfer and WebRTC limits also apply.

On a standalone VM, `ACME_CHALLENGE_DIR` can point at Certbot's HTTP-01 token directory. The HTTP listener serves only single token names from this confined directory, rejects escaping symlinks and oversized files, and retains redirects for other paths. Certbot webroot renewal can then run without stopping active measurements. See the [Bangalore VM bundle](../deploy/vm/blr/README.md) and [New Jersey Kubernetes manifests](../deploy/new-jersey/README.md) for the prepared deployment configurations.

An existing regional ingress remains an alternative to native TLS. Disable response compression, caching, buffering and request-body buffering on measurement routes. Allow the selected payload size and timeout at every hop. Keep HTTP traffic out of the UI hosting platform. Configure `TRUSTED_PROXY_CIDRS` only for actual HTTP proxy peers, and strip client-supplied forwarding headers at the first trusted proxy.

Expose the configured IPv4 UDP listener for packet-loss testing and set `WEBRTC_PUBLIC_IP` when advertising a NAT address. Otherwise explicitly disable it with an empty `WEBRTC_LISTEN_ADDR`. Verify real browser UDP echo across the intended network before launch.

Run the published image by digest as UID/GID 65532, with a read-only filesystem, no added capabilities and no privilege escalation. The image has no shell or package manager. Set memory, CPU, open-file and connection limits appropriate to the deployment. Mount any ASN database read-only. Use `/healthz` for liveness and `/readyz` for readiness; allow at least `REQUEST_TIMEOUT + 1s` for termination. Drain ingress traffic before stopping a replica.

## Simultaneous tests and overload

The defaults permit 64 active HTTP transfer requests per process, 16 per client IP, 64 WebRTC sessions and 16 WebRTC sessions per client IP. A four-stream test can occupy four HTTP slots. These are resource limits, not a claim that sixteen customers can each measure full line rate. Shared NAT users share client quotas. Multiple replicas do not share quota state.

`python3 tests/concurrency_smoke.py` starts its own loopback-only API. It verifies 1, 4 and 16 simulated tests with four streams each, exact download lengths and upload receipts, rejection at all 64 occupied slots, available health/latency probes, private metrics, and completion of 64 active uploads during graceful shutdown. Work is bounded to 16 one-MiB requests per stream and direction. The release preparation run passed with 20,044 KiB RSS at saturation. This brief local test excludes WAN latency, TLS, ingress and sustained bandwidth contention.

Before deployment approval, repeat tests through the actual HTTPS endpoint from independent clients, including shared NAT clients and IPv6 where configured. Record per-client throughput, CPU/RSS, errors, rejected transfers, probe latency and network utilization. Test overload, recovery and rolling termination. Select advertised capacity from measured available bandwidth and headroom; the production target and bandwidth are pending.

## Security and release checks

CI runs module verification, Go vet, race tests, concurrent transfer/shutdown checks and `govulncheck`. Weekly CI catches advisory changes. Protected pushes build once, smoke-test and scan that OCI artifact with Trivy, then publish and verify a keyless Cosign signature. The image includes SBOM and provenance attestations. Protected version tags publish matching image tags without the `v` prefix; deployments should pin the signed digest. Container images, release binaries and VM bundles target Linux AMD64. Published GitHub releases also receive the verified binary archive and its SHA-256 checksum file.

Go 1.27.1 and all imported runtime modules were checked against available updates. `govulncheck` found no affected symbols or imported packages. Its module inventory lists [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932), covering the unused `golang.org/x/crypto/openpgp` packages; these packages are absent from the application's dependency graph. There is no upstream fixed version. Do not add OpenPGP imports or suppress unrelated advisories.

Before promoting a release, require passing CI and CodeQL for its commit, inspect the container scan, verify the published digest/signature and retain the reports. Roll back by restoring the previously verified image digest and matching UI configuration. The service is stateless; there is no database migration.
