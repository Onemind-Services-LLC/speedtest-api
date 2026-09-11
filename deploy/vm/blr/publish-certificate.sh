#!/usr/bin/env bash
set -euo pipefail
# Certbot deploy hook; the API reads this pair every 30 seconds.
domain=blr.speedtest.onemindservices.cloud
case " ${RENEWED_DOMAINS:-} " in *" $domain "*) ;; *) exit 0 ;; esac
[[ $EUID -eq 0 ]] || { echo 'Certificate publication requires root.' >&2; exit 1; }
lineage=${RENEWED_LINEAGE:?Certbot must supply the certificate lineage}
[[ $lineage == "/etc/letsencrypt/live/$domain" ]] || { echo 'Unexpected certificate lineage.' >&2; exit 1; }
openssl x509 -in "$lineage/fullchain.pem" -noout -checkhost "$domain"
openssl x509 -in "$lineage/fullchain.pem" -noout -checkend 0
certificate_key=$(openssl x509 -in "$lineage/fullchain.pem" -pubkey -noout | openssl pkey -pubin -outform DER | sha256sum)
private_key=$(openssl pkey -in "$lineage/privkey.pem" -pubout -outform DER | sha256sum)
[[ $certificate_key == "$private_key" ]] || { echo 'Certificate/key mismatch.' >&2; exit 1; }
install -d -m 0750 -o root -g speedtest /etc/speedtest/tls
pair=$(mktemp -d /etc/speedtest/tls/pair.XXXXXXXX)
chown root:speedtest "$pair"
chmod 0750 "$pair"
install -m 0640 -o root -g speedtest "$lineage/fullchain.pem" "$pair/tls.crt"
install -m 0640 -o root -g speedtest "$lineage/privkey.pem" "$pair/tls.key"
link=/etc/speedtest/tls/.current.$$
trap 'rm -f "$link"' EXIT
ln -s "$(basename "$pair")" "$link"
mv -Tf "$link" /etc/speedtest/tls/current
