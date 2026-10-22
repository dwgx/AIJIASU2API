#!/usr/bin/env python3
"""Patch the embedded 1024-bit RSA modulus of the Linux AJiaSu client.

The client stores the server public modulus as a 309-digit ASCII decimal at
file offset 0x28a030 (Linux 4.2.3.0). This script generates a fresh 1024-bit
key whose modulus is also 309 decimal digits, overwrites those bytes in a
*copy* of the binary, and verifies that only that range changed.

Usage:
  patch_rsa.py <in-binary> <out-binary> <private-key.pem> [--offset 0x28a030]
"""
import argparse
import os
import subprocess
import sys

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import rsa


def gen_key():
    while True:
        k = rsa.generate_private_key(public_exponent=65537, key_size=1024)
        n = k.private_numbers().public_numbers.n
        dec = str(n).encode()
        if len(dec) == 309:
            return k, dec


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("src")
    ap.add_argument("dst")
    ap.add_argument("key")
    ap.add_argument("--offset", type=lambda x: int(x, 0), default=0x28A030)
    ap.add_argument("--length", type=int, default=309)
    args = ap.parse_args()

    if os.path.exists(args.key):
        with open(args.key, "rb") as f:
            priv = serialization.load_pem_private_key(f.read(), None)
        n = priv.private_numbers().public_numbers.n
        dec = str(n).encode()
        if len(dec) != args.length:
            sys.exit(f"existing key modulus has {len(dec)} digits, need {args.length}")
    else:
        priv, dec = gen_key()
        with open(args.key, "wb") as f:
            f.write(
                priv.private_bytes(
                    serialization.Encoding.PEM,
                    serialization.PrivateFormat.PKCS8,
                    serialization.NoEncryption(),
                )
            )

    data = bytearray(open(args.src, "rb").read())
    orig = bytes(data)
    old = bytes(data[args.offset : args.offset + args.length])
    if not old.isdigit():
        sys.exit(f"offset {args.offset:#x} does not hold a decimal string: {old[:16]!r}")
    print(f"old modulus ({len(old)} digits): {old[:20].decode()}...{old[-8:].decode()}")
    data[args.offset : args.offset + args.length] = dec
    with open(args.dst, "wb") as f:
        f.write(data)

    # verify only the intended bytes changed
    new = open(args.dst, "rb").read()
    diffs = [i for i in range(len(orig)) if orig[i] != new[i]]
    assert diffs and all(args.offset <= i < args.offset + args.length for i in diffs), diffs[:10]
    print(f"patched {len(diffs)} bytes at {args.offset:#x}; new modulus: {dec[:20].decode()}...{dec[-8:].decode()}")
    print(f"private key: {args.key}")


if __name__ == "__main__":
    main()
