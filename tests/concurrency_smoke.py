"""Bounded local concurrency and shutdown validation; never contacts a remote API."""
import concurrent.futures
import http.client
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import tempfile
import threading
import time


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def request(number, method, route, body=None, client="198.51.100.1"):
    conn = http.client.HTTPConnection("127.0.0.1", number, timeout=10)
    try:
        conn.request(method, route, body, {"X-Forwarded-For": client})
        response = conn.getresponse()
        return response.status, response.read(), response.headers
    finally:
        conn.close()


with tempfile.TemporaryDirectory(prefix="speedtest-concurrency-") as temp:
    binary = str(Path(temp) / "api")
    subprocess.run(["go", "build", "-trimpath", "-o", binary, "./cmd/speedtest-api"], check=True)
    public_port, metrics_port = port(), port()
    env = {**os.environ, "SPEEDTEST_ENV": "production", "REGION_ID": "load-test",
           "ALLOWED_ORIGINS": "https://ui.company.test", "LISTEN_ADDR": f"127.0.0.1:{public_port}",
           "METRICS_LISTEN_ADDR": f"127.0.0.1:{metrics_port}", "WEBRTC_LISTEN_ADDR": "",
           "MAX_CONCURRENT_TRANSFERS": "64", "MAX_TRANSFERS_PER_CLIENT": "16",
           "TRUSTED_PROXY_CIDRS": "127.0.0.1/32", "LOG_LEVEL": "error", "REQUEST_TIMEOUT": "10s"}
    process = subprocess.Popen([binary], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    held = []
    try:
        for _ in range(100):
            try:
                if request(public_port, "GET", "/healthz")[0] == 200:
                    break
            except OSError:
                pass
            time.sleep(0.05)
        else:
            raise RuntimeError("Local API failed to start")

        payload = os.urandom(1024 * 1024)
        reports = []
        for clients in [1, 4, 16]:
            report = {"simulated_tests": clients, "streams_per_test": 4}
            for direction in ["download", "upload"]:
                barrier = threading.Barrier(clients * 4)

                def stream(index):
                    conn = http.client.HTTPConnection("127.0.0.1", public_port, timeout=10)
                    try:
                        barrier.wait(timeout=10)
                        for _ in range(16):
                            headers = {"X-Forwarded-For": f"198.51.100.{index // 4 + 1}"}
                            if direction == "download":
                                conn.request("GET", f"/__down?bytes={len(payload)}", headers=headers)
                            else:
                                conn.request("POST", "/__up", payload, headers)
                            response = conn.getresponse()
                            body = response.read()
                            assert response.status == 200, response.status
                            if direction == "download":
                                assert len(body) == len(payload)
                            else:
                                assert json.loads(body)["bytes"] == len(payload)
                        return 16 * len(payload)
                    finally:
                        conn.close()

                start = time.monotonic()
                with concurrent.futures.ThreadPoolExecutor(max_workers=clients * 4) as pool:
                    total = sum(pool.map(stream, range(clients * 4)))
                report[direction] = {"verified_bytes": total, "seconds": round(time.monotonic() - start, 3)}
            reports.append(report)

        # Hold all 64 slots with incomplete uploads and verify rejection and probes.
        for index in range(64):
            sock = socket.create_connection(("127.0.0.1", public_port), timeout=10)
            sock.sendall((f"POST /__up HTTP/1.1\r\nHost: localhost\r\nContent-Length: 2\r\n"
                          f"X-Forwarded-For: 192.0.2.{index + 1}\r\nConnection: close\r\n\r\nx").encode())
            held.append(sock)
        for _ in range(100):
            metrics = request(metrics_port, "GET", "/metrics")[1].decode()
            if "speedtest_active_transfers 64\n" in metrics:
                break
            time.sleep(0.01)
        else:
            raise AssertionError("Did not occupy all 64 slots")
        busy = request(public_port, "POST", "/__up", b"x")
        assert busy[0] == 503 and busy[2]["Retry-After"] == "5"
        for route in ["/healthz", "/readyz", "/__down?bytes=0"]:
            assert request(public_port, "GET", route)[0] == 200
        assert request(public_port, "GET", "/metrics")[0] == 404
        rss = next(line.split()[1] for line in Path(f"/proc/{process.pid}/status").read_text().splitlines() if line.startswith("VmRSS:"))

        process.send_signal(signal.SIGTERM)
        for sock in held:
            sock.sendall(b"y")
            with sock.makefile("rb") as reader:
                response = reader.read()
            assert b"200 OK" in response and b'"bytes":2' in response
        process.wait(timeout=12)
        assert process.returncode == 0
        print(json.dumps({"transport": "local loopback HTTP", "phases": reports,
                          "saturated_slots": 64, "rss_kib_at_saturation": int(rss),
                          "overload_rejected": True, "probes_available": True,
                          "graceful_shutdown_completed_uploads": 64}, indent=2))
    finally:
        for sock in held:
            sock.close()
        if process.poll() is None:
            process.terminate()
            process.wait(timeout=12)
