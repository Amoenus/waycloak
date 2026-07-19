#!/usr/bin/env python3
"""Minimal Waycloak adapter using only the Python standard library."""

import json
import ipaddress
import os
import pathlib
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PROTOCOL = "networking.waycloak.io/adapter/v1alpha1"
DELIVERY_VERSION = "networking.waycloak.io/v1alpha1"
ready = threading.Event()


def parse_time(value):
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def select_lease(document, lease_name):
    if document.get("apiVersion") != DELIVERY_VERSION or not document.get("podUID"):
        raise ValueError("invalid lease document identity")
    matches = [
        record
        for record in document.get("leases", [])
        if record.get("applicationPortMode") == "ProviderAssigned"
        and (not lease_name or record.get("name") == lease_name)
    ]
    if len(matches) != 1:
        raise ValueError("exactly one current provider-assigned lease is required")
    record = matches[0]
    if ipaddress.ip_address(record["publicAddress"]).version != 4:
        raise ValueError("provider public address must be IPv4")
    if parse_time(record["expiresAt"]) <= datetime.now(timezone.utc):
        raise ValueError("lease is expired")
    return record


def apply_port(path, port):
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile("w", dir=path.parent, delete=False) as output:
        output.write(f"{port}\n")
        temporary = pathlib.Path(output.name)
    temporary.replace(path)
    if path.read_text(encoding="utf-8").strip() != str(port):
        raise RuntimeError("application did not retain the exact port")


def reconcile(endpoint, lease_name, application_file):
    with urllib.request.urlopen(endpoint, timeout=2) as response:
        document = json.load(response)
    record = select_lease(document, lease_name)
    apply_port(application_file, record["applicationPort"])
    acknowledgement = {
        "apiVersion": PROTOCOL,
        "podUID": document["podUID"],
        "leaseIdentity": record["identity"],
        "generation": record["generation"],
        "applicationPort": record["applicationPort"],
    }
    identity = urllib.parse.quote(record["identity"], safe="")
    request = urllib.request.Request(
        f"{endpoint.rstrip('/')}/{identity}/ack",
        data=json.dumps(acknowledgement, separators=(",", ":")).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=2) as response:
        if response.status != 204:
            raise RuntimeError("acknowledgement was not accepted")


class HealthHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/readyz":
            self.send_error(404)
        elif ready.is_set():
            self.send_response(200)
            self.end_headers()
        else:
            self.send_error(503)

    def log_message(self, _format, *_args):
        pass


def main():
    protocol = os.environ.get("WAYCLOAK_ADAPTER_PROTOCOL", "")
    if protocol != PROTOCOL:
        raise SystemExit(f"unsupported adapter protocol {protocol!r}")
    endpoint = os.environ["WAYCLOAK_LEASE_ENDPOINT"]
    lease_name = os.environ.get("WAYCLOAK_LEASE_NAME", "")
    application_file = pathlib.Path(os.environ.get("WAYCLOAK_SAMPLE_PORT_FILE", "/application/listen-port"))
    health_port = int(os.environ.get("WAYCLOAK_SAMPLE_HEALTH_PORT", "9811"))
    server = ThreadingHTTPServer(("127.0.0.1", health_port), HealthHandler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    delay = 1.0
    while True:
        try:
            reconcile(endpoint, lease_name, application_file)
            ready.set()
            delay = 1.0
        except (KeyError, ValueError, RuntimeError, OSError, urllib.error.URLError, json.JSONDecodeError):
            ready.clear()
            delay = min(delay * 2, 15.0)
        time.sleep(delay)


if __name__ == "__main__":
    main()
