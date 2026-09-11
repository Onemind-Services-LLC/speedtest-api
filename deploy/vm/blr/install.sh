#!/usr/bin/env bash
set -euo pipefail
bundle=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
usage() { echo 'Usage: sudo ./install.sh PUBLIC_IPV4 ACME_EMAIL [CLOUDFLARE_CREDENTIALS_INI]'; }
if [[ ${1:-} == --help ]]; then usage; exit 0; fi
[[ $# -ge 2 && $# -le 3 && $EUID -eq 0 ]] || { usage >&2; exit 1; }
public_ip=$1
acme_email=$2
credentials=
if [[ $# -eq 3 ]]; then credentials=$(realpath -- "$3"); fi
python3 - "$public_ip" "$acme_email" <<'PY'
import ipaddress,sys
ip=ipaddress.IPv4Address(sys.argv[1])
if not ip.is_global: raise SystemExit('A public IPv4 address is required.')
if '@' not in sys.argv[2] or any(c.isspace() for c in sys.argv[2]): raise SystemExit('A valid ACME account email is required.')
PY
for executable in systemctl certbot curl openssl sha256sum useradd install python3; do
 command -v "$executable" >/dev/null || { echo "Missing prerequisite: $executable" >&2; exit 1; }
done
if [[ -n $credentials ]]; then
 [[ -f $credentials && -r $credentials ]] || { echo 'Cloudflare credentials are not readable.' >&2; exit 1; }
 plugins=$(certbot plugins --text)
 [[ $plugins == *dns-cloudflare* ]] || { echo 'Install the Certbot Cloudflare DNS plugin first.' >&2; exit 1; }
fi
[[ -x /usr/bin/certbot && -x /usr/bin/curl ]] || { echo 'The systemd units require /usr/bin/certbot and /usr/bin/curl.' >&2; exit 1; }
case $(uname -m) in x86_64) architecture=amd64 ;; aarch64|arm64) architecture=arm64 ;; *) echo 'Unsupported CPU architecture.' >&2; exit 1 ;; esac
[[ $(cat "$bundle/ARCH") == "$architecture" ]] || { echo 'Bundle architecture does not match this VM.' >&2; exit 1; }
(cd "$bundle" && sha256sum --check --strict SHA256SUMS)
build=$(cat "$bundle/BUILD_ID")
[[ $build =~ ^[a-f0-9]{12}$ ]] || { echo 'Invalid bundle ID.' >&2; exit 1; }
if ! id speedtest >/dev/null 2>&1; then
 useradd --system --user-group --home-dir /nonexistent --shell /usr/sbin/nologin speedtest
fi
[[ $(id -u speedtest) -ne 0 ]] || { echo 'The speedtest account must be non-root.' >&2; exit 1; }
install -d -m 0755 /opt/speedtest/releases /var/lib/speedtest /usr/local/libexec
install -d -m 0750 -o root -g speedtest /etc/speedtest
install -d -m 0755 /var/lib/speedtest-acme/.well-known/acme-challenge
install -m 0755 -o root -g root "$bundle/publish-certificate.sh" /usr/local/libexec/speedtest-publish-certificate
install -m 0644 -o root -g root "$bundle/bootstrap-http.py" /usr/local/libexec/speedtest-bootstrap-http.py
if [[ -n $credentials ]]; then
 install -d -m 0700 /etc/speedtest/acme
 if [[ ! $credentials -ef /etc/speedtest/acme/cloudflare.ini ]]; then
  install -m 0600 -o root -g root "$credentials" /etc/speedtest/acme/cloudflare.ini
 fi
 chmod 0600 /etc/speedtest/acme/cloudflare.ini
 authentication=(--dns-cloudflare --dns-cloudflare-credentials /etc/speedtest/acme/cloudflare.ini --dns-cloudflare-propagation-seconds 60)
elif systemctl is-active --quiet speedtest-api.service; then
 authentication=(--webroot --webroot-path /var/lib/speedtest-acme)
else
 # Use persistent HTTP while bootstrapping; the native API later serves the
 # same webroot and certificate renewal leaves measurement listeners running.
 systemd-run --unit=speedtest-acme-bootstrap --property=RuntimeMaxSec=300 --collect \
  /usr/bin/python3 /usr/local/libexec/speedtest-bootstrap-http.py /var/lib/speedtest-acme
 trap 'systemctl stop speedtest-acme-bootstrap.service || true' EXIT
 authentication=(--webroot --webroot-path /var/lib/speedtest-acme)
fi
certbot certonly --non-interactive --agree-tos --email "$acme_email" \
 "${authentication[@]}" --key-type ecdsa --keep-until-expiring \
 --cert-name blr.speedtest.onemindservices.cloud -d blr.speedtest.onemindservices.cloud \
 --deploy-hook /usr/local/libexec/speedtest-publish-certificate
if systemctl is-active --quiet speedtest-acme-bootstrap.service; then
 systemctl stop speedtest-acme-bootstrap.service
fi
trap - EXIT
# Also install a still-valid existing certificate when certbot does not renew it.
RENEWED_DOMAINS=blr.speedtest.onemindservices.cloud \
 RENEWED_LINEAGE=/etc/letsencrypt/live/blr.speedtest.onemindservices.cloud \
 /usr/local/libexec/speedtest-publish-certificate
install -d -m 0755 "/opt/speedtest/releases/$build"
# Replace atomically so reapplying a bundle also works while this binary runs.
install -m 0755 "$bundle/speedtest-api" "/opt/speedtest/releases/$build/.speedtest-api.$$"
mv -Tf "/opt/speedtest/releases/$build/.speedtest-api.$$" "/opt/speedtest/releases/$build/speedtest-api"
install -m 0644 "$bundle/BUILD.json" "/opt/speedtest/releases/$build/BUILD.json"
install -m 0644 "$bundle/asn.mmdb" "/var/lib/speedtest/.asn.mmdb.$$"
mv -Tf "/var/lib/speedtest/.asn.mmdb.$$" /var/lib/speedtest/asn.mmdb
install -m 0644 "$bundle/ASN-NOTICE" /var/lib/speedtest/ASN-NOTICE
python3 - "$bundle/api.env.example" "$public_ip" <<'PY'
from pathlib import Path
import sys
Path('/etc/speedtest/api.env').write_text(Path(sys.argv[1]).read_text().replace('VM_PUBLIC_IPV4',sys.argv[2]))
PY
chown root:speedtest /etc/speedtest/api.env
chmod 0640 /etc/speedtest/api.env
link=/opt/speedtest/.current.$$
trap 'rm -f "$link"' EXIT
ln -s "releases/$build" "$link"
mv -Tf "$link" /opt/speedtest/current
install -m 0644 "$bundle/speedtest-api.service" "$bundle/speedtest-certificates.service" "$bundle/speedtest-certificates.timer" /etc/systemd/system/
systemctl daemon-reload
systemctl enable speedtest-api.service speedtest-certificates.timer
systemctl restart speedtest-api.service
systemctl start speedtest-certificates.timer
curl --fail --silent --show-error http://127.0.0.1:8080/readyz
printf '\nInstalled Bangalore Speedtest. Configure its DNS-only A record and validate HTTPS/UDP before adding it to the UI.\n'
