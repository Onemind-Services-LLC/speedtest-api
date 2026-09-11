# Bangalore VM deployment

Deployed for region `blr`, display name **Bangalore**, at **https://blr.speedtest.onemindservices.cloud**. This bundle runs the API directly on a Linux VM with its own public IPv4 address. It does not require Kubernetes, Docker, an ingress controller, or an HTTP reverse proxy. The assigned VM is `ubuntu@163.227.207.223` (Ubuntu 24.04, AMD64), and its DNS-only A record points there. Bangalore creates no resources in the New Jersey production cluster.

## Bundle contents

The Linux AMD64 archive contains a static API binary, checksums and build provenance, the September 2026 DB-IP ASN Lite database and attribution, service units, an installer, a certificate publication hook, and the UI region entry. AMD64 is validated on the deployed VM. Creating a deployment bundle does not publish a GitHub release.

## Host preparation

Use a Linux distribution with systemd. The installer expects Bash, Python 3, OpenSSL, curl, Certbot 2.3 or newer. On Debian or Ubuntu, install `ca-certificates curl openssl python3 certbot` using the system package manager. Inspect the VM's existing ports, firewall, architecture, SSH access and service users before installation.

Create a DNS-only A record for `blr.speedtest.onemindservices.cloud` pointing to the VM's actual public IPv4 address. Keep Cloudflare proxying disabled. Allow incoming TCP 80 and 443 and UDP 8081 in the VM and provider firewalls. Preserve the actual SSH port and operator access. Health port 8080 and metrics port 9090 bind to loopback only and must not be exposed publicly.

The installer deliberately leaves firewall and DNS changes to the deployment operator. It rejects a private IP or an archive for the wrong CPU architecture and verifies every bundled file before installation. It never prints the Cloudflare credential contents.

## HTTPS and renewal

By default the installer obtains the first certificate using HTTP-01 through a temporary HTTP/1.1 webroot listener, then starts the API with the same webroot. The bootstrap listener shuts down before the API starts and has a five-minute runtime limit. The API serves only bounded ACME token files from a confined directory; ordinary browser traffic still redirects to the UI. Subsequent renewals leave measurement listeners running. No Cloudflare API credential is required.

After verifying the archive checksum, extract it into a directory accessible only to the operator and run:

```sh
sudo ./install.sh 163.227.207.223 ACME_EMAIL
```

DNS-01 remains optional: install `python3-certbot-dns-cloudflare` and pass a root-readable credentials INI as a third argument. A token should be restricted to DNS editing for the certificate's zone; see the [Certbot Cloudflare plugin](https://certbot-dns-cloudflare.readthedocs.io/en/stable/). This alternative also renews without stopping measurements.

The API runs as the `speedtest` system user. It accepts HTTPS on port 443 and redirects HTTP on port 80. Only top-level browser GET navigation redirects to the UI; tests, uploads and preflights retain their API behavior. The service has only the privilege needed to bind those ports, a read-only filesystem, a 512 MiB memory limit and a two-core CPU quota. These limits are conservative starting values, not a validated bandwidth or simultaneous-user capacity.

The socket-family allowlist includes `AF_NETLINK` because Go/Pion uses it to enumerate network interfaces during WebRTC negotiation. Omitting it causes valid browser offers to fail before any UDP exchange, even when port 8081 is open. The service has no `CAP_NET_ADMIN` capability.

The dedicated timer checks certificate renewal twice daily. On a dedicated host with no other Certbot certificates, disable the package-wide `certbot.timer` to avoid duplicate scheduling. The hook verifies the hostname, expiry and matching key, then publishes a complete pair through an atomic symlink. The API reloads certificates every 30 seconds without restarting active transfers. Certificate keys and DNS credentials stay on the VM, outside this bundle. Direct public connections use the real socket address, with both HTTP forwarding-header trust and PROXY protocol disabled.

## Verification and UI activation

After installation, check `systemctl status speedtest-api speedtest-certificates.timer`, `/readyz`, the HTTPS certificate, CORS from the production UI, exact upload/download receipts, and the client IP/ASN response. Test renewal with `sudo certbot renew --cert-name blr.speedtest.onemindservices.cloud --dry-run`. Complete a browser speed test and UDP packet-loss exchange from an external client before advertising the region.

Append the object in `region.json` to the existing Vercel region registry after these checks pass. Keep the New Jersey entry and existing test settings. The region ID is lowercase `blr` because API identifiers accept lowercase letters, digits and hyphens.

On 11 September 2026, the deployed VM passed HTTPS identity, CORS, exact 1 MiB upload/download, client-IP spoof rejection, browser redirect and private-metrics checks. Certbot's renewal dry run passed while the API stayed running. External Firefox and Chrome probes established the WebRTC data channel and exchanged UDP test messages after the socket-family correction. Bangalore was then added alongside New Jersey in Vercel's production region configuration, using the existing UI source commit. These are functional checks from one client network, not a regional load-capacity certification.

The ASN database is DB-IP ASN Lite, redistributed unchanged under CC BY 4.0; see `ASN-NOTICE`. `asnProvider: "db-ip"` in the UI entry supplies attribution. Refresh the reviewed database and checksum monthly.

## Update and rollback

The installer retains binaries under `/opt/speedtest/releases/` and switches `/opt/speedtest/current` to the selected build. An update restarts the single API process after certificate/configuration preparation. Allow active tests to finish first. For rollback, point `current` at the previous compatible build, restore its matching `/etc/speedtest/api.env`, and restart `speedtest-api`. No database migration is required. Protect the certificate and configuration directories when backing up the VM.
