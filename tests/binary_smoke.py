"""Verify the packaged release binary itself using bounded local HTTP transfers."""
import http.client
import json
import os
from pathlib import Path
import socket
import struct
import subprocess
import sys
import tarfile
import tempfile
import time


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


directory = Path(sys.argv[1]).resolve()
subprocess.run(["sha256sum", "--check", "--strict", "binary-SHA256SUMS"], cwd=directory, check=True)
archives = list(directory.glob("*.tar.gz"))
assert len(archives) == 1, "Expected one Linux AMD64 archive"
with tempfile.TemporaryDirectory(prefix="speedtest-binary-") as temp:
    with tarfile.open(archives[0]) as archive:
        assert set(archive.getnames()) == {"speedtest-api", "README.md", "BUILD.json"}
        archive.extractall(temp, filter="data")
    root = Path(temp)
    binary = root / "speedtest-api"
    header = binary.read_bytes()[:20]
    assert header[:6] == b"\x7fELF\x02\x01" and struct.unpack("<H", header[18:20])[0] == 62, "Expected x86-64 ELF"
    program_headers = subprocess.check_output(["readelf", "-l", binary], text=True)
    assert "INTERP" not in program_headers, "Binary requires a dynamic linker"
    metadata = json.loads((root / "BUILD.json").read_text())
    expected_version = Path("internal/version/VERSION").read_text().strip()
    assert metadata["version"] == expected_version
    assert metadata["sourceCommit"] == subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    port = free_port()
    env = {k: v for k, v in os.environ.items() if k in {"PATH", "LANG", "TZ"}}
    env.update({"SPEEDTEST_ENV": "development", "LISTEN_ADDR": f"127.0.0.1:{port}",
                "WEBRTC_LISTEN_ADDR": "", "LOG_LEVEL": "error"})
    process = subprocess.Popen([binary], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    def request(method, path, body=None):
        connection = http.client.HTTPConnection("127.0.0.1", port, timeout=3)
        try:
            connection.request(method, path, body)
            response = connection.getresponse()
            result = response.read()
            assert response.status == 200, (path, response.status)
            return result
        finally:
            connection.close()

    try:
        for attempt in range(100):
            try:
                request("GET", "/readyz")
                break
            except OSError:
                assert process.poll() is None, "Packaged binary exited before readiness"
                time.sleep(0.05)
        else:
            raise AssertionError("Packaged binary did not become ready")
        info = json.loads(request("GET", "/v1/info"))
        assert info["applicationVersion"] == expected_version, info
        payload = os.urandom(1024 * 1024)
        assert len(request("GET", f"/__down?bytes={len(payload)}")) == len(payload)
        assert json.loads(request("POST", "/__up", payload))["bytes"] == len(payload)
    finally:
        process.terminate()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
            raise
    assert process.returncode == 0, "Packaged binary did not shut down cleanly"
print("Linux AMD64 archive: checksum, static ELF, version, transfers and shutdown passed")
