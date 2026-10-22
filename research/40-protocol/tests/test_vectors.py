#!/usr/bin/env python3
"""Vector tests for the AJiaSu FV reference implementation.

Runs with pytest or directly:

    python research/40-protocol/tests/test_vectors.py

The signing vectors are produced independently by compiling the OpenAJS
``FVSign.java`` (vendored verbatim under ``tests/java``) with JDK 21 and running
``VectorHarness``.  The Python implementation must match it byte for byte.

No network access is performed.
"""

from __future__ import annotations

import functools
import subprocess
import sys
import tempfile
from pathlib import Path

from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.asymmetric import padding, rsa

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "reference"))

import fv_proto  # noqa: E402  (path setup must happen first)

JAVA_DIR = Path(__file__).resolve().parent / "java"
FV_SIGN_JAVA = JAVA_DIR / "us" / "cubk" / "openajs" / "api" / "crypto" / "FVSign.java"
HARNESS_JAVA = JAVA_DIR / "VectorHarness.java"

SECRET = fv_proto.OPEN_AJS_SECRET
TIMESTAMP = "1761177600000"

LOGIN_BODY = (
    "_site=ajs&_v=4.11.10.0&cmd=ClientApiLogin%2FLogin"
    "&password=p%40ss&trigger=login&username=test%40example.com"
)


def _hx(value: str) -> str:
    return value.encode("utf-8").hex()


def _sign_cases() -> list[tuple[str, str, str]]:
    """(secret, body, timestamp) tuples shared with the Java harness."""
    return [
        (SECRET, "", TIMESTAMP),
        (SECRET, "abc", TIMESTAMP),
        (SECRET, LOGIN_BODY, TIMESTAMP),
        (SECRET, "中文&ünïcode", "1761177600123"),
    ]


def _query_cases() -> list[dict[str, str]]:
    return [
        {"b": "2", "a": "1", "c": "a b&c=d/é"},
        {"_os_dim": "w=1080&h=2400&d=440", "_site": "ajs"},
        {"z": "", "A": "~-._"},
    ]


@functools.lru_cache(maxsize=1)
def _java_vectors() -> dict[str, dict[int, tuple[str, ...]]]:
    """Compile FVSign + VectorHarness with javac and return its output."""
    if not FV_SIGN_JAVA.is_file() or not HARNESS_JAVA.is_file():
        raise AssertionError("vendored Java harness is missing under tests/java")

    sign_cases = _sign_cases()
    query_cases = _query_cases()

    records: list[str] = []
    for secret, body, timestamp in sign_cases:
        records.append("S\t%s\t%s\t%s" % (_hx(secret), _hx(body), _hx(timestamp)))
    for params in query_cases:
        fields = ["Q"]
        for key, value in params.items():
            fields.extend((_hx(key), _hx(value)))
        records.append("\t".join(fields))

    with tempfile.TemporaryDirectory(prefix="ajs-vectors-") as tmp:
        tmp_path = Path(tmp)
        classes = tmp_path / "classes"
        classes.mkdir()
        tsv = tmp_path / "vectors.tsv"
        tsv.write_text("\n".join(records) + "\n", encoding="utf-8")

        compile_result = subprocess.run(
            [
                "javac",
                "-encoding",
                "UTF-8",
                "-d",
                str(classes),
                str(FV_SIGN_JAVA),
                str(HARNESS_JAVA),
            ],
            capture_output=True,
            text=True,
            encoding="utf-8",
        )
        if compile_result.returncode != 0:
            raise AssertionError(
                "javac failed (is JDK 21 on PATH?):\n"
                + compile_result.stdout
                + compile_result.stderr
            )

        run_result = subprocess.run(
            ["java", "-cp", str(classes), "VectorHarness", str(tsv)],
            capture_output=True,
            text=True,
            encoding="utf-8",
        )
        if run_result.returncode != 0:
            raise AssertionError(
                "VectorHarness failed:\n" + run_result.stdout + run_result.stderr
            )

    parsed: dict[str, dict[int, tuple[str, ...]]] = {"S": {}, "Q": {}}
    for line in run_result.stdout.splitlines():
        if not line:
            continue
        parts = line.split("\t")
        kind = parts[0]
        int(parts[1])  # validate the harness index, but use per-kind order
        index = len(parsed[kind])
        if kind == "S":
            parsed["S"][index] = (parts[2], parts[3])
        elif kind == "Q":
            parsed["Q"][index] = (bytes.fromhex(parts[2]).decode("utf-8"),)
        else:
            raise AssertionError("unexpected harness line: %r" % line)
    if len(parsed["S"]) != len(sign_cases) or len(parsed["Q"]) != len(query_cases):
        raise AssertionError("harness returned an unexpected number of vectors")
    return parsed


def test_sign_and_dual_hash_match_java() -> None:
    vectors = _java_vectors()["S"]
    for index, (secret, body, timestamp) in enumerate(_sign_cases()):
        java_dual, java_sign = vectors[index]
        assert fv_proto.dual_hash(body.encode("utf-8")).hex() == java_dual, (
            "dualHash mismatch for case %d" % index
        )
        assert fv_proto.sign(secret, body, timestamp) == java_sign, (
            "sign mismatch for case %d" % index
        )


def test_build_query_matches_java() -> None:
    vectors = _java_vectors()["Q"]
    for index, params in enumerate(_query_cases()):
        (java_query,) = vectors[index]
        assert fv_proto.build_query(params) == java_query, (
            "query mismatch for case %d: %r != %r"
            % (index, fv_proto.build_query(params), java_query)
        )


def test_frame_round_trip() -> None:
    header_iv = bytes(range(16))
    body_iv = bytes(range(16, 32))
    session_key = bytes(range(32, 48))
    text = "r=ok\nsid=ABC123\n"
    body_plain = b"foo=bar\n"

    frame = fv_proto.build_frame(header_iv, text)
    body_ct = fv_proto.aes_ctr(session_key, body_iv, body_plain)

    # Client form: the frame starts with the cleartext header IV.
    client_form = fv_proto.parse_response(
        frame + body_ct, header_iv, session_key, body_iv
    )
    # Server form: no IV prefix, header ciphertext starts at offset 0.
    server_form = fv_proto.parse_response(
        frame[16:] + body_ct, header_iv, session_key, body_iv
    )
    for fields in (client_form, server_form):
        assert fields["r"] == "ok"
        assert fields["sid"] == "ABC123"
        assert fields["foo"] == "bar"


def test_aes_ctr_nist_sp800_38a() -> None:
    # NIST SP 800-38A, F.5.1 CTR-AES128.Encrypt.
    key = bytes.fromhex("2b7e151628aed2a6abf7158809cf4f3c")
    iv = bytes.fromhex("f0f1f2f3f4f5f6f7f8f9fafbfcfdfeff")
    plaintext = bytes.fromhex(
        "6bc1bee22e409f96e93d7e117393172a"
        "ae2d8a571e03ac9c9eb76fac45af8e51"
        "30c81c46a35ce411e5fbc1191a0a52ef"
        "f69f2445df4f9b17ad2b417be66c3710"
    )
    expected = bytes.fromhex(
        "874d6191b620e3261bef6864990db6ce"
        "9806f66b7970fdff8617187bb9fffdff"
        "5ae4df3edbd5d35e5b4f09020db03eab"
        "1e031dda2fbe03d1792170a0f3009cee"
    )
    assert fv_proto.aes_ctr(key, iv, plaintext) == expected
    assert fv_proto.aes_ctr(key, iv, expected) == plaintext


def test_rsa_oaep_shape_and_local_roundtrip() -> None:
    session_key = bytes.fromhex("00112233445566778899aabbccddeeff")

    # The OpenAJS modulus is 1024-bit, so ciphertexts are 128 bytes.  A
    # 256-byte expectation would require a 2048-bit key, which is not what
    # FvCrypto.java:21 contains.
    assert fv_proto.FV_RSA_MODULUS.bit_length() == 1024
    ct1 = fv_proto.rsa_oaep_encrypt(session_key)
    ct2 = fv_proto.rsa_oaep_encrypt(session_key)
    assert len(ct1) == 128
    assert len(ct2) == 128
    assert ct1 != ct2, "OAEP encryption must be randomized"

    block = fv_proto.oaep_block(session_key)
    assert block.startswith(b"OAEP")
    assert len(block) == 4 + 128

    # Validate that the helper's padding parameters are OAEP/MGF1 with the
    # requested digest, using a local key we can decrypt with.  The server's
    # private key is unavailable, so this is the only direct check possible.
    private_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    for hash_name, algorithm in (
        ("SHA-1", hashes.SHA1()),
        ("SHA-256", hashes.SHA256()),
    ):
        ciphertext = fv_proto._rsa_oaep_encrypt_with_key(
            private_key.public_key(), session_key, hash_name
        )
        recovered = private_key.decrypt(
            ciphertext,
            padding.OAEP(
                mgf=padding.MGF1(algorithm=algorithm),
                algorithm=algorithm,
                label=None,
            ),
        )
        assert recovered == session_key


def test_auth_body_serialization() -> None:
    fields = {
        "t": "1761177600",
        "c": "au",
        "u": "user",
        "p": "pa\nss",
        "ce": "",
    }
    expected = b"t=1761177600\nc=au\nu=user\np=pa ss\nce=\n"
    assert fv_proto.build_auth_body(fields) == expected


def test_socks5_connect_request() -> None:
    assert (
        fv_proto.socks5_connect_request("example.com", 443)
        == bytes.fromhex("050100030b6578616d706c652e636f6d01bb")
    )


def main() -> int:
    tests = [
        test_sign_and_dual_hash_match_java,
        test_build_query_matches_java,
        test_frame_round_trip,
        test_aes_ctr_nist_sp800_38a,
        test_rsa_oaep_shape_and_local_roundtrip,
        test_auth_body_serialization,
        test_socks5_connect_request,
    ]
    passed = 0
    for test in tests:
        try:
            test()
        except Exception as exc:  # noqa: BLE001 - report and fail the process
            print("FAIL %s: %s" % (test.__name__, exc))
            return 1
        passed += 1
        print("ok %s" % test.__name__)
    print("PASS %d" % passed)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
