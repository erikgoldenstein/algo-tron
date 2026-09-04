from __future__ import annotations

import random
import socket
import time

from .model import BotParams, RuntimeFaults


def encode_packet(packet_type: str, *args: object, newline: bool = True) -> bytes:
    line = "|".join(str(value) for value in (packet_type, *args))
    return (line + ("\n" if newline else "")).encode()


def send_packet(
    sock: socket.socket,
    packet_type: str,
    *args: object,
    params: BotParams | None = None,
    rng: random.Random | None = None,
) -> None:
    data = encode_packet(packet_type, *args)
    if params is None or rng is None:
        sock.sendall(data)
        return
    if params.bandwidth_kbps > 0:
        time.sleep(len(data) * 8 / (params.bandwidth_kbps * 1000))
    if rng.random() >= params.fragment_chance or len(data) < 3:
        sock.sendall(data)
        return
    cut_points = sorted(rng.sample(range(1, len(data)), min(3, len(data) - 1)))
    start = 0
    for end in (*cut_points, len(data)):
        sock.sendall(data[start:end])
        start = end
        if rng.random() < 0.5:
            time.sleep(rng.uniform(0.001, 0.015))


def connect(host: str, port: int, timeout: float, family_name: str) -> socket.socket:
    host = host[1:-1] if host.startswith("[") and host.endswith("]") else host
    family = {"ipv4": socket.AF_INET, "ipv6": socket.AF_INET6}.get(family_name, socket.AF_UNSPEC)
    addresses = socket.getaddrinfo(host, port, family, socket.SOCK_STREAM)
    last_error: OSError | None = None
    for address_family, socktype, proto, _, address in addresses:
        sock = socket.socket(address_family, socktype, proto)
        sock.settimeout(timeout)
        try:
            sock.connect(address)
            sock.settimeout(None)
            sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
            return sock
        except OSError as error:
            last_error = error
            sock.close()
    raise ConnectionError(f"could not connect to {host}:{port}: {last_error}")


def should_drop_move(
    params: BotParams,
    runtime: RuntimeFaults,
    now: float,
    rng: random.Random,
    group_outage_until: float,
) -> bool:
    if now < group_outage_until or now < runtime.burst_until:
        return True
    if rng.random() < params.burst_loss_chance:
        runtime.burst_until = now + rng.uniform(params.burst_loss_min_ms, params.burst_loss_max_ms) / 1000
        return True
    return rng.random() < params.packet_loss
