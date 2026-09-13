"""Real PTY smoke: opt-in, actions, process/log panes, terminal restoration."""
import fcntl
import os
import pty
import select
import signal
import socket
import struct
import subprocess
import tempfile
import termios
import time
import urllib.error
import urllib.request


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


with tempfile.TemporaryDirectory() as tmp:
    proxy_port, app_port = port(), port()
    config = os.path.join(tmp, "config.yaml")
    with open(config, "w") as out:
        out.write(f"""port: {proxy_port}
stopTimeout: 2s
startTimeout: 5s
apps:
  api:
    pwd: {tmp}
    launch: python3 -u -m http.server {app_port} --bind 127.0.0.1
    path: /api
    port: {app_port}
    idle: 0
""")

    refused = subprocess.run(["./lazywrap-test", "tui", "-c", config], capture_output=True)
    assert refused.returncode != 0 and b"interactive terminal" in refused.stderr

    # Normal invocation remains a plain server and never writes terminal escapes.
    normal = subprocess.Popen(["./lazywrap-test", "-c", config], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    try:
        deadline = time.monotonic() + 10
        while True:
            try:
                with socket.create_connection(("127.0.0.1", proxy_port), timeout=.1):
                    break
            except OSError:
                assert time.monotonic() < deadline
                time.sleep(.1)
        normal.send_signal(signal.SIGTERM)
        stdout, stderr = normal.communicate(timeout=10)
        assert normal.returncode == 0 and b"\x1b[" not in stdout + stderr
    finally:
        if normal.poll() is None:
            normal.kill()
            normal.wait()

    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 120, 0, 0))
    original = termios.tcgetattr(slave)
    child = subprocess.Popen(["./lazywrap-test", "tui", "-c", config], stdin=slave, stdout=slave, stderr=slave)
    transcript = bytearray()

    def until(text, timeout=15):
        deadline = time.monotonic() + timeout
        while text not in transcript:
            assert child.poll() is None, (child.returncode, transcript.decode(errors="replace"))
            assert time.monotonic() < deadline, transcript.decode(errors="replace")
            if select.select([master], [], [], .1)[0]:
                transcript.extend(os.read(master, 65536))

    def key(text):
        transcript.clear()
        os.write(master, text.encode())

    try:
        until(b"[stopped]")
        key("S")
        until(b"[running]")
        with urllib.request.urlopen(f"http://127.0.0.1:{proxy_port}/api/", timeout=5) as response:
            assert response.status == 200
        key("l3")
        until(b"GET /")
        key("3p2\r")
        until(b" PPID ")
        key("2x")
        until(b"[stopped]")
        try:
            urllib.request.urlopen(f"http://127.0.0.1:{proxy_port}/api/", timeout=5)
            raise AssertionError("manual stop was undone by traffic")
        except urllib.error.HTTPError as exc:
            assert exc.code == 503
        key("S")
        until(b"[running]")
        key("r")
        until(b"restart complete")
        key("k")
        until(b"Confirm kill api")
        key("n")
        until(b"[running]")
        key("k")
        until(b"Confirm kill api")
        key("y")
        until(b"[stopped]")
        key("S")
        until(b"[running]")
        key("q")
        until(b"Confirm quit")
        os.write(master, b"y")
        child.wait(timeout=15)
        assert child.returncode == 0
        assert termios.tcgetattr(slave) == original, "terminal mode not restored"
        try:
            socket.create_connection(("127.0.0.1", app_port), timeout=.2).close()
            raise AssertionError("app survived TUI shutdown")
        except ConnectionRefusedError:
            pass
        print("PTY smoke passed: plain mode, TUI, logs, tree, stop/start/restart/kill, quit, terminal restoration")
    finally:
        if child.poll() is None:
            child.send_signal(signal.SIGTERM)
            try:
                child.wait(timeout=10)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait()
        os.close(master)
        os.close(slave)
