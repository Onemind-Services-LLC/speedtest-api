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
    "--env", "WEBRTC_LISTEN_ADDR=", image,
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
        assert json.load(response)["region"]["id"] == "local"
    request = urllib.request.Request(f"{base}/__down?bytes=131072", headers={"Origin": "http://localhost:3000"})
    with urllib.request.urlopen(request, timeout=5) as response:
        payload = response.read()
        assert len(payload) == 131072
        assert response.headers["X-Speedtest-Region"] == "local"
        assert response.headers["X-Request-ID"]
        assert response.headers["Access-Control-Allow-Origin"] == "http://localhost:3000"
        assert "no-transform" in response.headers["Cache-Control"]
        assert response.headers.get("Content-Encoding") is None
    request = urllib.request.Request(f"{base}/__up", data=payload, headers={"Content-Type": "application/octet-stream"})
    with urllib.request.urlopen(request, timeout=5) as response:
        assert json.load(response) == {"bytes": len(payload), "regionId": "local"}
    user = subprocess.check_output(["docker", "inspect", "--format", "{{.Config.User}}", container], text=True).strip()
    assert user == "65532:65532"
    print("Container smoke test passed: non-root, read-only, health, CORS, download and upload.")
finally:
    subprocess.run(["docker", "stop", "--time", "5", container], check=False, stdout=subprocess.DEVNULL)
