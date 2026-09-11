# New Jersey deployment

Region **`nnj`**, display name **New Jersey**, serves **https://nnj.speedtest.onemindservices.cloud** in the production-apps cluster's `speedtest` namespace.

The API terminates HTTPS directly. There is no speedtest ingress controller or Ingress resource. The namespace has exactly three Services:

| Service | Purpose |
| --- | --- |
| `speedtest-api-public` | Public TCP 80/443, native API TLS and redirects |
| `speedtest-api-packet-loss` | Public UDP 8081, WebRTC packet-loss measurements |
| `speedtest-api-metrics` | Private TCP 9090, restricted to Prometheus |

The two public Services share OpenStack load balancer `e9e7598d-182b-42a8-a6d4-c42ce8605f22` at **38.67.240.85**. ExternalDNS owns the DNS-only A record through the public Service; Cloudflare proxying is explicitly disabled. The old `speedtest-ingress`, `speedtest-api-udp` and internal `speedtest-api` Services were removed, together with the old ingress/controller/redirect manifests and namespace-scoped controller permissions.

## Configuration and application

These manifests describe this specific cluster, including its existing load-balancer ID, network ranges, monitoring labels, image-pull Secret, and DNS issuer. The source copy is in `speedtest-api/deploy/new-jersey`; `/home/asaharan/clusters/production-apps/speedtest` is the matching operational copy. Neither copy contains credentials, a kubeconfig, certificate keys or the ASN database itself. Preserve the existing namespace's Rancher project assignment and `onemindservices` pull Secret.

```sh
kubectl --kubeconfig /home/asaharan/clusters/production-apps/kubeconfig.yaml apply --dry-run=server -k /home/asaharan/clusters/production-apps/speedtest
kubectl --kubeconfig /home/asaharan/clusters/production-apps/kubeconfig.yaml apply -k /home/asaharan/clusters/production-apps/speedtest
kubectl --kubeconfig /home/asaharan/clusters/production-apps/kubeconfig.yaml -n speedtest rollout status deployment/speedtest-api
```

Update both manifest copies together. Applying this Kustomization does not delete unrelated resources. It contains no old ingress manifests to recreate.

The API is pinned to release `v0.1.1`: `registry.onemindservices.com/speedtest/api:0.1.1@sha256:476c3cf9c34924d58810f6f77c8428e5a16870b7fa603ef62b07971d658918ec`. The release image passed container smoke tests, vulnerability scanning and signature verification before deployment. The ASN init image remains pinned to the reviewed September 2026 DB-IP database.

## TLS, identity and limits

The existing cert-manager Certificate and DNS-01 ClusterIssuer renew the mounted Secret. The API reloads its certificate every 30 seconds. Native TLS requires matching SNI and Host and permits TLS 1.2 or newer with HTTP/1.1. HTTP redirects to HTTPS; top-level browser GET navigation redirects to `https://speedtest.onemindservices.cloud/`. Fetch/XHR, uploads and preflight requests retain their API routes.

The TCP load balancer supplies PROXY v2. Only `172.16.3.0/24` is trusted, and NetworkPolicy restricts the native public pod ports to that range. HTTP forwarding-header trust is disabled. Metrics remain private; the ordinary HTTP health listener is not exposed through a Service. API egress remains denied, with stateful responses allowed.

Public listeners limit each source to 200 requests/second with burst 400, cap active requests at 128 per process and bound the client-rate table to 10,000 entries. HTTP transfer limits remain 64 globally and 16 per client; WebRTC limits remain 64 globally and 16 per client. Payloads are capped at 64 MiB. DB-IP ASN identification uses the local read-only database and makes no external lookup requests.

## Operations

Keep one replica and `Recreate`: HTTP signaling and UDP must reach the same process. Updates or node failures can interrupt tests. Do not enable HPA without changing packet-loss routing. The 512 MiB memory and two-core CPU limits do not establish sustained WAN capacity.

The direct-LB comparison used four Standard tests per path from one workstation against the same API process. Median download/upload was 84.5/167.6 Mbps through ingress and 85.3/164.3 Mbps directly, with no observed CPU throttling. The ingress was removed as an architectural choice; these measurements did not show a meaningful throughput increase.

For rollback, restore a compatible API image/configuration pair that supports native TLS. The pre-cutover release image requires an external TLS terminator and is not a drop-in rollback for these Services. Never delete the public Services as part of an application update: removing the last attached Service releases the cloud load balancer and IP. Preserve the shared ExternalDNS domain filters.
