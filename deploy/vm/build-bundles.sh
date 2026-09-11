#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
[[ $# -ge 1 && $# -le 2 ]] || { echo 'Usage: build-bundles.sh ASN_MMDB [OUTPUT_DIRECTORY]' >&2; exit 1; }
asn_database=$(realpath -- "$1")
output=${2:-$repo/dist/vm}
mkdir -p "$output"
output=$(realpath -- "$output")
expected_asn=ab07c764a10c4f8c2f3539377fa86e5c928243084aafd359d1be4cb8543d7406
printf '%s  %s\n' "$expected_asn" "$asn_database" | sha256sum --check --status || { echo 'ASN database checksum does not match the reviewed September 2026 dataset.' >&2; exit 1; }
cd "$repo"
go mod verify
for architecture in amd64 arm64; do
 stage=$(mktemp -d "$output/.build-$architecture.XXXXXXXX")
 trap 'rm -rf "$stage"' EXIT
 CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go build -buildvcs=false -trimpath -ldflags='-s -w' -o "$stage/speedtest-api" ./cmd/speedtest-api
 for file in api.env.example region.json ASN-NOTICE bootstrap-http.py install.sh publish-certificate.sh speedtest-api.service speedtest-certificates.service speedtest-certificates.timer README.md; do
  cp "deploy/vm/blr/$file" "$stage/$file"
 done
 cp "$asn_database" "$stage/asn.mmdb"
 printf '%s\n' "$architecture" > "$stage/ARCH"
 sha256sum "$stage/speedtest-api" | cut -c1-12 > "$stage/BUILD_ID"
 python3 - "$stage" "$architecture" <<'PY'
from pathlib import Path
import hashlib,json,subprocess,sys
stage=Path(sys.argv[1]);root=Path.cwd()
files=sorted([Path('go.mod'),Path('go.sum'),Path('internal/version/VERSION'),*Path('cmd').rglob('*.go'),*Path('internal').rglob('*.go')])
tree=hashlib.sha256()
for path in files: tree.update(str(path).encode()+b'\0'+hashlib.sha256(path.read_bytes()).digest())
(stage/'BUILD.json').write_text(json.dumps({'sourceCommit':subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip(),'uncommittedChanges':bool(subprocess.check_output(['git','status','--porcelain'],text=True).strip()),'goSourceSHA256':tree.hexdigest(),'goVersion':subprocess.check_output(['go','version'],text=True).strip(),'architecture':sys.argv[2],'region':'blr','applicationVersion':Path('internal/version/VERSION').read_text().strip(),'releasePublished':False},indent=2)+'\n')
PY
 python3 - "$stage" <<'PY_HASH'
from pathlib import Path
import hashlib,sys
stage=Path(sys.argv[1])
lines=[hashlib.sha256(p.read_bytes()).hexdigest()+'  '+p.name for p in sorted(stage.iterdir()) if p.is_file()]
(stage/'SHA256SUMS').write_text('\n'.join(lines)+'\n')
PY_HASH
 (cd "$stage" && sha256sum --check --status SHA256SUMS)
 tar --sort=name --owner=0 --group=0 --numeric-owner --mtime=@0 -C "$stage" -cf - . | gzip -n > "$output/speedtest-blr-linux-$architecture.tar.gz"
 rm -rf "$stage"
 trap - EXIT
 done
(cd "$output" && sha256sum speedtest-blr-linux-amd64.tar.gz speedtest-blr-linux-arm64.tar.gz > SHA256SUMS && sha256sum --check SHA256SUMS)
