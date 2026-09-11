#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo"
output=${1:-dist/binaries}
mkdir -p "$output"
output=$(realpath -- "$output")
version=$(cat internal/version/VERSION)
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo 'Invalid application version.' >&2; exit 1; }
if [[ ${GITHUB_REF:-} == refs/tags/* ]]; then
 [[ ${GITHUB_REF#refs/tags/} == "v$version" ]] || { echo 'Tag does not match application version.' >&2; exit 1; }
fi
stage=$(mktemp -d "$output/.build.XXXXXXXX")
trap 'rm -rf "$stage"' EXIT
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags='-s -w' -o "$stage/speedtest-api" ./cmd/speedtest-api
cp README.md "$stage/README.md"
python3 - "$stage" "$version" <<'PY'
import json,pathlib,subprocess,sys
def git(*args): return subprocess.check_output(['git',*args],text=True).strip()
metadata={'version':sys.argv[2],'sourceCommit':git('rev-parse','HEAD'),
          'uncommittedChanges':bool(git('status','--porcelain')),
          'goVersion':subprocess.check_output(['go','version'],text=True).strip(),
          'os':'linux','architecture':'amd64','cgoEnabled':False}
(pathlib.Path(sys.argv[1])/'BUILD.json').write_text(json.dumps(metadata,indent=2)+'\n')
PY
archive="speedtest-api_v${version}_linux_amd64.tar.gz"
tar --sort=name --owner=0 --group=0 --numeric-owner --mtime=@0 -C "$stage" -cf - speedtest-api README.md BUILD.json | gzip -n > "$output/$archive"
(cd "$output" && sha256sum "$archive" > binary-SHA256SUMS && sha256sum --check --strict binary-SHA256SUMS)
