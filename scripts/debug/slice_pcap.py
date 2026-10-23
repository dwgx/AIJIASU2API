#!/usr/bin/env python3
"""Slice a classic pcap by time, host, port, protocol or TLS SNI.

The default ``native`` backend parses the pcap with the standard library only,
so it works on hosts without Wireshark (HMC is one). The optional ``tshark``
backend shells out to ``tshark`` when it is installed; it is not exercised by
the HMC dry run and is marked [UNVERIFIED] in README.md.

Determinism: packets are copied byte-for-byte in capture order with their
original timestamps and lengths, and the input global header is reused
unchanged. Two runs over the same input and filters produce identical bytes
(modulo the output path, which is not embedded).

Time semantics: ``--start`` is inclusive, ``--end`` is exclusive
(``start <= ts < end``). Times accept RFC 3339 (``2026-10-23T14:00:00Z``) or
epoch seconds.

Exit codes: 0 success, 2 usage, 3 unsupported input/backend.
"""

from __future__ import annotations

import argparse
import hashlib
import ipaddress
import json
import os
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Iterator, Optional

PCAP_MAGICS = {
    b"\xd4\xc3\xb2\xa1": ("<", 1_000),   # little-endian, microseconds
    b"\xa1\xb2\xc3\xd4": (">", 1_000),   # big-endian, microseconds
    b"\x4d\x3c\xb2\xa1": ("<", 1),       # little-endian, nanoseconds
    b"\xa1\xb2\x3c\x4d": (">", 1),       # big-endian, nanoseconds
}

# LINKTYPE_* values we can dissect without libpcap.
LINKTYPE_NULL = 0
LINKTYPE_ETHERNET = 1
LINKTYPE_RAW = 101
LINKTYPE_LINUX_SLL = 113
LINKTYPE_LINUX_SLL2 = 276


class SliceError(Exception):
    """A user-facing failure; the message is printed without a traceback."""


@dataclass(frozen=True)
class PcapHeader:
    raw: bytes
    endian: str
    frac_scale: int  # 1_000 for microsecond files, 1 for nanosecond files
    snaplen: int
    linktype: int


@dataclass(frozen=True)
class Record:
    ts_ns: int
    incl_len: int
    orig_len: int
    data: bytes


@dataclass(frozen=True)
class PacketInfo:
    src: str
    dst: str
    sport: Optional[int]
    dport: Optional[int]
    proto: str
    sni: Optional[str]


def be16(data: bytes) -> int:
    return struct.unpack(">H", data)[0]


def parse_time_ns(value: str) -> int:
    text = value.strip()
    if not text:
        raise argparse.ArgumentTypeError("empty time")
    try:
        seconds = float(text)
    except ValueError:
        try:
            stamp = datetime.fromisoformat(text.replace("Z", "+00:00"))
        except ValueError as exc:
            raise argparse.ArgumentTypeError(
                f"time {value!r} is neither RFC 3339 nor epoch seconds"
            ) from exc
        if stamp.tzinfo is None:
            stamp = stamp.replace(tzinfo=timezone.utc)
        return int(round(stamp.timestamp() * 1_000_000_000))
    return int(round(seconds * 1_000_000_000))


def iso_utc(ts_ns: Optional[int]) -> Optional[str]:
    if ts_ns is None:
        return None
    seconds, nanos = divmod(ts_ns, 1_000_000_000)
    stamp = datetime.fromtimestamp(seconds, tz=timezone.utc)
    return stamp.strftime("%Y-%m-%dT%H:%M:%S") + f".{nanos // 1000:06d}Z"


def read_header(handle) -> PcapHeader:
    raw = handle.read(24)
    if len(raw) < 24:
        raise SliceError("input is shorter than a pcap global header")
    magic = raw[:4]
    if magic not in PCAP_MAGICS:
        if magic == b"\x0a\x0d\x0d\x0a":
            raise SliceError(
                "input is pcapng; convert it with `editcap -F pcap in out.pcap` "
                "or `tshark -F pcap -r in -w out.pcap` first"
            )
        raise SliceError(f"unsupported capture magic {magic.hex()}")
    endian, frac_scale = PCAP_MAGICS[magic]
    snaplen, linktype = struct.unpack(endian + "II", raw[16:24])
    return PcapHeader(raw=raw, endian=endian, frac_scale=frac_scale,
                      snaplen=snaplen, linktype=linktype)


def iter_records(handle, header: PcapHeader) -> Iterator[Record]:
    packet_header = struct.Struct(header.endian + "IIII")
    while True:
        head = handle.read(16)
        if not head:
            return
        if len(head) < 16:
            raise SliceError("truncated packet header at end of file")
        ts_sec, ts_frac, incl_len, orig_len = packet_header.unpack(head)
        data = handle.read(incl_len)
        if len(data) < incl_len:
            raise SliceError(
                f"truncated packet body: header says {incl_len} bytes, "
                f"only {len(data)} remain"
            )
        yield Record(
            ts_ns=ts_sec * 1_000_000_000 + ts_frac * header.frac_scale,
            incl_len=incl_len,
            orig_len=orig_len,
            data=data,
        )


def network_payload(linktype: int, data: bytes) -> Optional[tuple[int, bytes]]:
    """Return (ip_version, bytes starting at the IP header) or None."""
    if linktype == LINKTYPE_ETHERNET:
        if len(data) < 14:
            return None
        ethertype = be16(data[12:14])
        offset = 14
        while ethertype in (0x8100, 0x88A8, 0x9100):
            if len(data) < offset + 4:
                return None
            ethertype = be16(data[offset + 2:offset + 4])
            offset += 4
        if ethertype == 0x0800:
            return 4, data[offset:]
        if ethertype == 0x86DD:
            return 6, data[offset:]
        return None
    if linktype == LINKTYPE_LINUX_SLL:
        if len(data) < 16:
            return None
        proto = be16(data[14:16])
        return _ip_version(proto), data[16:]
    if linktype == LINKTYPE_LINUX_SLL2:
        if len(data) < 20:
            return None
        proto = be16(data[0:2])
        return _ip_version(proto), data[20:]
    if linktype == LINKTYPE_RAW:
        return _ip_nibble(data), data
    if linktype == LINKTYPE_NULL:
        if len(data) < 4:
            return None
        return _ip_nibble(data[4:]), data[4:]
    return None


def _ip_version(ethertype: int) -> int:
    if ethertype == 0x0800:
        return 4
    if ethertype == 0x86DD:
        return 6
    return 0


def _ip_nibble(data: bytes) -> int:
    if not data:
        return 0
    version = data[0] >> 4
    return version if version in (4, 6) else 0


def parse_packet(linktype: int, data: bytes) -> Optional[PacketInfo]:
    parsed = network_payload(linktype, data)
    if parsed is None:
        return None
    version, ip = parsed
    if version == 4:
        return _parse_ipv4(ip)
    if version == 6:
        return _parse_ipv6(ip)
    return None


def _parse_ipv4(ip: bytes) -> Optional[PacketInfo]:
    if len(ip) < 20 or ip[0] >> 4 != 4:
        return None
    header_len = (ip[0] & 0x0F) * 4
    if header_len < 20 or len(ip) < header_len:
        return None
    fragment = be16(ip[6:8]) & 0x1FFF
    src = str(ipaddress.IPv4Address(ip[12:16]))
    dst = str(ipaddress.IPv4Address(ip[16:20]))
    proto_num = ip[9]
    transport = ip[header_len:]
    if fragment:
        return PacketInfo(src, dst, None, None, _proto_name(proto_num), None)
    return _parse_transport(src, dst, proto_num, transport)


def _parse_ipv6(ip: bytes) -> Optional[PacketInfo]:
    if len(ip) < 40 or ip[0] >> 4 != 6:
        return None
    src = str(ipaddress.IPv6Address(ip[8:24]))
    dst = str(ipaddress.IPv6Address(ip[24:40]))
    next_header = ip[6]
    offset = 40
    # Hop-by-hop, routing, destination options.
    while next_header in (0, 43, 60):
        if len(ip) < offset + 2:
            return None
        next_header, ext_len = ip[offset], (ip[offset + 1] + 1) * 8
        offset += ext_len
        if offset > len(ip):
            return None
    if next_header == 44:  # fragment
        if len(ip) < offset + 8:
            return None
        fragment = be16(ip[offset + 2:offset + 4]) >> 3
        next_header = ip[offset]
        offset += 8
        if fragment:
            return PacketInfo(src, dst, None, None, _proto_name(next_header), None)
    if next_header == 51:  # authentication header
        if len(ip) < offset + 2:
            return None
        ah_len = (ip[offset + 1] + 2) * 4
        next_header = ip[offset]
        offset += ah_len
    return _parse_transport(src, dst, next_header, ip[offset:])


def _proto_name(proto_num: int) -> str:
    if proto_num == 6:
        return "tcp"
    if proto_num == 17:
        return "udp"
    return f"ip/{proto_num}"


def _parse_transport(src: str, dst: str, proto_num: int,
                     transport: bytes) -> Optional[PacketInfo]:
    if proto_num == 6:
        if len(transport) < 20:
            return PacketInfo(src, dst, None, None, "tcp", None)
        sport, dport = be16(transport[0:2]), be16(transport[2:4])
        header_len = (transport[12] >> 4) * 4
        payload = transport[header_len:] if header_len >= 20 else b""
        return PacketInfo(src, dst, sport, dport, "tcp", tls_sni(payload))
    if proto_num == 17:
        if len(transport) < 8:
            return PacketInfo(src, dst, None, None, "udp", None)
        sport, dport = be16(transport[0:2]), be16(transport[2:4])
        return PacketInfo(src, dst, sport, dport, "udp", None)
    return PacketInfo(src, dst, None, None, _proto_name(proto_num), None)


def tls_sni(payload: bytes) -> Optional[str]:
    """Extract the first server_name from a TLS ClientHello, or None.

    A ClientHello split across TCP segments is not reassembled; this is a
    slicer, not a protocol reassembler. Ports 443/4443/8443 are the intended
    users.
    """
    if len(payload) < 5 or payload[0] != 0x16:
        return None
    record_len = be16(payload[3:5])
    body = payload[5:5 + record_len]
    if len(body) < 4 or body[0] != 0x01:
        return None
    pos = 4
    if len(body) < pos + 34:
        return None
    pos += 2 + 32  # client_version + random
    session_len = body[pos]
    pos += 1 + session_len
    if len(body) < pos + 2:
        return None
    cipher_len = be16(body[pos:pos + 2])
    pos += 2 + cipher_len
    if len(body) < pos + 1:
        return None
    compression_len = body[pos]
    pos += 1 + compression_len
    if len(body) < pos + 2:
        return None
    extensions_len = be16(body[pos:pos + 2])
    pos += 2
    end = min(len(body), pos + extensions_len)
    while pos + 4 <= end:
        ext_type = be16(body[pos:pos + 2])
        ext_len = be16(body[pos + 2:pos + 4])
        pos += 4
        if pos + ext_len > end:
            return None
        if ext_type == 0x0000:
            if ext_len < 5:
                return None
            cursor = pos + 2  # skip server_name_list length
            name_type = body[cursor]
            name_len = be16(body[cursor + 1:cursor + 3])
            cursor += 3
            if name_type != 0 or cursor + name_len > pos + ext_len:
                return None
            raw = body[cursor:cursor + name_len]
            try:
                return raw.decode("ascii")
            except UnicodeDecodeError:
                return None
        pos += ext_len
    return None


def resolve_hosts(values: list[str], resolve: bool) -> list[str]:
    hosts: set[str] = set()
    for value in values:
        text = value.strip()
        try:
            hosts.add(str(ipaddress.ip_address(text)))
            continue
        except ValueError:
            pass
        if not resolve:
            raise SliceError(
                f"--host {value!r} is not an IP literal; add --resolve to "
                "resolve it once, or pass the IP"
            )
        try:
            infos = socket.getaddrinfo(text, None, proto=socket.IPPROTO_TCP)
        except OSError as exc:
            raise SliceError(f"cannot resolve --host {value!r}: {exc}") from exc
        for info in infos:
            hosts.add(info[4][0])
    return sorted(hosts)


def make_matcher(args, hosts: list[str], ports: set[int],
                 snis: set[str]):
    def matches(record: Record) -> bool:
        if args.start_ns is not None and record.ts_ns < args.start_ns:
            return False
        if args.end_ns is not None and record.ts_ns >= args.end_ns:
            return False
        needs_dissect = bool(hosts or ports or snis or args.proto != "any")
        if not needs_dissect:
            return True
        info = parse_packet(args.linktype, record.data)
        if info is None:
            return False
        if args.proto != "any" and info.proto != args.proto:
            return False
        if hosts and info.src not in hosts and info.dst not in hosts:
            return False
        if ports:
            if info.sport not in ports and info.dport not in ports:
                return False
        if snis:
            if info.sni is None or info.sni.lower() not in snis:
                return False
        return True

    return matches


def build_tshark_filter(args, hosts: list[str], ports: set[int],
                        snis: set[str]) -> str:
    clauses: list[str] = []
    if args.start_ns is not None:
        clauses.append(f"frame.time_epoch >= {args.start_ns / 1e9:.9f}")
    if args.end_ns is not None:
        clauses.append(f"frame.time_epoch < {args.end_ns / 1e9:.9f}")
    if args.proto != "any":
        clauses.append(args.proto)
    for host in hosts:
        clauses.append(f"ip.addr == {host}")
    for port in sorted(ports):
        clauses.append(f"(tcp.port == {port} || udp.port == {port})")
    for sni in sorted(snis):
        clauses.append(f'tls.handshake.extensions_server_name == "{sni}"')
    return " && ".join(clauses) if clauses else "frame"


def slice_native(args, header: PcapHeader, out_path: Path,
                 hosts: list[str], ports: set[int], snis: set[str]) -> dict:
    matcher = make_matcher(args, hosts, ports, snis)
    packets_in = 0
    packets_out = 0
    bytes_out = 0
    first_ts: Optional[int] = None
    last_ts: Optional[int] = None
    tmp_fd, tmp_name = tempfile.mkstemp(
        prefix=out_path.name + ".tmp.", dir=str(out_path.parent)
    )
    try:
        with os.fdopen(tmp_fd, "wb") as out, open(args.input, "rb") as handle:
            out.write(header.raw)
            handle.seek(len(header.raw))
            for record in iter_records(handle, header):
                packets_in += 1
                if not matcher(record):
                    continue
                out.write(struct.pack(
                    header.endian + "IIII",
                    record.ts_ns // 1_000_000_000,
                    (record.ts_ns % 1_000_000_000) // header.frac_scale,
                    record.incl_len,
                    record.orig_len,
                ))
                out.write(record.data)
                packets_out += 1
                bytes_out += record.incl_len
                if first_ts is None:
                    first_ts = record.ts_ns
                last_ts = record.ts_ns
        os.replace(tmp_name, out_path)
    except BaseException:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        raise
    return {
        "packets_in": packets_in,
        "packets_out": packets_out,
        "bytes_out": bytes_out,
        "first_ts": iso_utc(first_ts),
        "last_ts": iso_utc(last_ts),
    }


def slice_tshark(args, out_path: Path, hosts: list[str], ports: set[int],
                 snis: set[str]) -> dict:
    tshark = shutil.which("tshark")
    if not tshark:
        raise SliceError("tshark backend requested but tshark is not installed")
    display = build_tshark_filter(args, hosts, ports, snis)
    tmp_fd, tmp_name = tempfile.mkstemp(
        prefix=out_path.name + ".tmp.", dir=str(out_path.parent)
    )
    os.close(tmp_fd)
    try:
        completed = subprocess.run(
            [tshark, "-r", args.input, "-Y", display, "-F", "pcap", "-w", tmp_name],
            text=True, capture_output=True, check=False,
        )
        if completed.returncode != 0:
            raise SliceError(
                "tshark failed: " + (completed.stderr.strip() or "no stderr")
            )
        os.replace(tmp_name, out_path)
    except BaseException:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        raise
    return {
        "packets_in": None,
        "packets_out": None,
        "bytes_out": None,
        "first_ts": None,
        "last_ts": None,
        "tshark_filter": display,
    }


def slice_editcap(args, out_path: Path, hosts: list[str], ports: set[int],
                  snis: set[str]) -> dict:
    if hosts or ports or snis or args.proto != "any":
        raise SliceError(
            "editcap backend supports time bounds only; use the native backend "
            "for host/port/proto/SNI filters"
        )
    if args.start_ns is None and args.end_ns is None:
        raise SliceError("editcap backend needs --start or --end")
    editcap = shutil.which("editcap")
    if not editcap:
        raise SliceError("editcap backend requested but editcap is not installed")
    tmp_fd, tmp_name = tempfile.mkstemp(
        prefix=out_path.name + ".tmp.", dir=str(out_path.parent)
    )
    os.close(tmp_fd)
    command = [editcap]
    if args.start_ns is not None:
        command += ["-A", iso_utc(args.start_ns)]
    if args.end_ns is not None:
        command += ["-B", iso_utc(args.end_ns)]
    command += ["-F", "pcap", args.input, tmp_name]
    try:
        completed = subprocess.run(command, text=True, capture_output=True, check=False)
        if completed.returncode != 0:
            raise SliceError(
                "editcap failed: " + (completed.stderr.strip() or "no stderr")
            )
        os.replace(tmp_name, out_path)
    except BaseException:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        raise
    try:
        packets_in = count_records(args.input)
        packets_out = count_records(str(out_path))
    except SliceError:
        packets_in = None
        packets_out = None
    return {
        "packets_in": packets_in,
        "packets_out": packets_out,
        "bytes_out": out_path.stat().st_size,
        "first_ts": None,
        "last_ts": None,
        "editcap_command": command,
        "note": "editcap -B is inclusive; native uses start <= ts < end",
    }


def count_records(path: str) -> int:
    with open(path, "rb") as handle:
        header = read_header(handle)
        return sum(1 for _ in iter_records(handle, header))


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def check_report() -> dict:
    return {
        "python": sys.version.split()[0],
        "tshark": shutil.which("tshark"),
        "editcap": shutil.which("editcap"),
        "capinfos": shutil.which("capinfos"),
        "tcpdump": shutil.which("tcpdump"),
        "backend_default": "native",
        "linktypes": {
            "ethernet": LINKTYPE_ETHERNET,
            "raw": LINKTYPE_RAW,
            "linux_sll": LINKTYPE_LINUX_SLL,
            "linux_sll2": LINKTYPE_LINUX_SLL2,
            "null": LINKTYPE_NULL,
        },
    }


def parse_args(argv: Optional[list[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Slice a classic pcap by time, host, port, protocol or TLS SNI.",
        epilog="Times are start-inclusive, end-exclusive. Hosts are IP literals "
               "unless --resolve is given.",
    )
    parser.add_argument("input", nargs="?", help="input .pcap")
    parser.add_argument("-o", "--output", help="output .pcap (default: <input>.slice.pcap)")
    parser.add_argument("--start", dest="start_ns", type=parse_time_ns, metavar="TIME",
                        help="inclusive start (RFC 3339 or epoch)")
    parser.add_argument("--end", dest="end_ns", type=parse_time_ns, metavar="TIME",
                        help="exclusive end (RFC 3339 or epoch)")
    parser.add_argument("--host", action="append", default=[], metavar="IP",
                        help="match src or dst; repeatable")
    parser.add_argument("--port", action="append", default=[], type=int,
                        help="match src or dst port; repeatable")
    parser.add_argument("--proto", choices=["tcp", "udp", "any"], default="any")
    parser.add_argument("--sni", action="append", default=[], metavar="NAME",
                        help="match TLS ClientHello server_name; repeatable")
    parser.add_argument("--resolve", action="store_true",
                        help="resolve non-literal --host values once")
    parser.add_argument("--backend", choices=["native", "tshark", "editcap"], default="native")
    parser.add_argument("--force", action="store_true", help="overwrite the output")
    parser.add_argument("--quiet", action="store_true", help="do not print the JSON summary")
    parser.add_argument("--check", action="store_true",
                        help="print tool/linktype capabilities and exit")
    args = parser.parse_args(argv)
    if args.check:
        return args
    if not args.input:
        parser.error("an input pcap is required unless --check is used")
    args.input = str(Path(args.input))
    if args.start_ns is not None and args.end_ns is not None and args.start_ns >= args.end_ns:
        parser.error("--start must be earlier than --end")
    if args.port:
        for port in args.port:
            if not 0 <= port <= 65535:
                parser.error(f"port {port} is outside 0..65535")
    return args


def main(argv: Optional[list[str]] = None) -> int:
    args = parse_args(argv)
    if args.check:
        print(json.dumps(check_report(), indent=2, sort_keys=True))
        return 0

    input_path = Path(args.input)
    if not input_path.is_file():
        raise SliceError(f"input {input_path} is not a file")
    output_path = Path(args.output) if args.output else input_path.with_suffix(".slice.pcap")
    if output_path.resolve() == input_path.resolve():
        raise SliceError("output must differ from input")
    if output_path.exists() and not args.force:
        raise SliceError(f"output {output_path} exists; pass --force to overwrite")
    output_path.parent.mkdir(parents=True, exist_ok=True)

    hosts = resolve_hosts(args.host, args.resolve)
    ports = set(args.port)
    snis = {value.strip().lower() for value in args.sni if value.strip()}

    with open(input_path, "rb") as handle:
        header = read_header(handle)
    args.linktype = header.linktype

    if args.backend == "native":
        stats = slice_native(args, header, output_path, hosts, ports, snis)
    elif args.backend == "tshark":
        stats = slice_tshark(args, output_path, hosts, ports, snis)
    else:
        stats = slice_editcap(args, output_path, hosts, ports, snis)

    summary = {
        "input": str(input_path),
        "output": str(output_path),
        "backend": args.backend,
        "linktype": header.linktype,
        "filters": {
            "start": iso_utc(args.start_ns),
            "end": iso_utc(args.end_ns),
            "hosts": hosts,
            "ports": sorted(ports),
            "proto": args.proto,
            "sni": sorted(snis),
        },
        **stats,
        "sha256": sha256_file(output_path),
        "size": output_path.stat().st_size,
    }
    if not args.quiet:
        print(json.dumps(summary, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except SliceError as exc:
        print(f"slice_pcap: {exc}", file=sys.stderr)
        sys.exit(3)
    except BrokenPipeError:
        sys.exit(0)
