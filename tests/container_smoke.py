"""Verify the actual non-root image with streaming HTTP requests."""
import json
import subprocess
import sys
import time
import urllib.error
import urllib.request

image = sys.argv[1]
container = subprocess.check_output([
    "docker", "run", "--detach", "--rm", "--read-only", "--cap-drop=ALL",
    "--security-opt=no-new-privileges", "--publish", "127.0.0.1::8080",
    "--env", "WEBRTC_LISTEN_ADDR=", "--env", "SPEEDTEST_ENV=production",
    "--env", "REGION_ID=smoke", "--env", "ALLOWED_ORIGINS=https://ui.company.test",
    "--env", "METRICS_LISTEN_ADDR=127.0.0.1:9090", image,
], text=True).strip()
try:
    port = int(subprocess.check_output(["docker", "port", container, "8080/tcp"], text=True).strip().rsplit(":", 1)[1])
    base = f"http://127.0.0.1:{port}"
    for attempt in range(50):
        try:
            with urllib.request.urlopen(f"{base}/healthz", timeout=1) as response:
                assert response.status == 200
            break
        except (urllib.error.URLError, TimeoutError):
            time.sleep(0.1)
    else:
        raise RuntimeError("Container did not become healthy")
    with urllib.request.urlopen(f"{base}/v1/info", timeout=5) as response:
        assert json.load(response)["region"]["id"] == "smoke"
    with urllib.request.urlopen(f"{base}/v1/network", timeout=5) as response:
        network = json.load(response)
        assert network["regionId"] == "smoke"
        assert network["family"] == 4
        assert network["asn"] is None
        assert network["asnStatus"] == "private-address"
        assert network["ip"]
    request = urllib.request.Request(f"{base}/__down?bytes=131072", headers={"Origin": "https://ui.company.test"})
    with urllib.request.urlopen(request, timeout=5) as response:
        payload = response.read()
        assert len(payload) == 131072
        assert response.headers["X-Speedtest-Region"] == "smoke"
        assert response.headers["X-Request-ID"]
        assert response.headers["Access-Control-Allow-Origin"] == "https://ui.company.test"
        assert "no-transform" in response.headers["Cache-Control"]
        assert response.headers.get("Content-Encoding") is None
    request = urllib.request.Request(f"{base}/__up", data=payload, headers={"Content-Type": "application/octet-stream"})
    with urllib.request.urlopen(request, timeout=5) as response:
        assert json.load(response) == {"bytes": len(payload), "regionId": "smoke"}
    try:
        urllib.request.urlopen(f"{base}/metrics", timeout=5)
        raise AssertionError("Metrics exposed on public listener")
    except urllib.error.HTTPError as error:
        assert error.code == 404
    user = subprocess.check_output(["docker", "inspect", "--format", "{{.Config.User}}", container], text=True).strip()
    assert user == "65532:65532"
    print("Container smoke test passed: production configuration, non-root, read-only, private metrics, health, network identity, CORS, download and upload.")
finally:
    subprocess.run(["docker", "stop", "--time", "5", container], check=False, stdout=subprocess.DEVNULL)
