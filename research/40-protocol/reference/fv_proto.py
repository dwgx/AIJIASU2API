"""Language-neutral reference implementation of the AJiaSu FV protocol.

Derived from the public-domain OpenAJS Java client
(https://github.com/cubk1/OpenAJS, commit cae4308bef4de609a1091b790696290eb5b69e9b).
See research/40-protocol/PROTOCOL.md for the prose specification.

This module never performs network I/O and never contacts the real service.

Only the Python standard library and ``cryptography`` are required.
"""

from __future__ import annotations

import base64
from collections.abc import Mapping

from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.asymmetric import padding, rsa
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

# ---------------------------------------------------------------------------
# Constants taken verbatim from OpenAJS.
# ---------------------------------------------------------------------------

#: Signing secret hard-coded in AjsClient.java:59.  Its use by the shipping
#: 4.9.6.0 Windows client is [UNKNOWN]; neither PE nor ELF contains it as a
#: plain string.
OPEN_AJS_SECRET = "84:C1:12:45:0B:68:F9:B2:06:95:00:44:74:37:55:71:60:B5:98:31"

#: RSA modulus from FvCrypto.serverPublicKey().  Despite the 309 decimal digits
#: it is a 1024-bit key: RSA ciphertexts are 128 bytes, not 256.
FV_RSA_MODULUS = int(
    "1416011879059269094446447302864096667554953000507215643981997690807466163391"
    "4855668321904801712001874801642809935915320565136266400128789072253579946670"
    "0786136519566714160932079339659299698611914846217596226121056961277268141789"
    "9054863805399611020533984356321467107313619228365266584517939210730887411383"
    "57721"
)

#: Public exponent, FvCrypto.java:22.
FV_RSA_EXPONENT = 65537

#: Block magic prepended to the RSA ciphertext, FvConnection.oaepBlock().
OAEP_BLOCK_MAGIC = b"OAEP"

#: 2-byte length field allows at most this many UTF-8 text bytes per frame.
MAX_FRAME_TEXT = 0xFFFF

_UNRESERVED = frozenset(
    b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
)

_HASHES = {
    "SHA-1": hashes.SHA1,
    "SHA-224": hashes.SHA224,
    "SHA-256": hashes.SHA256,
    "SHA-384": hashes.SHA384,
    "SHA-512": hashes.SHA512,
}


# ---------------------------------------------------------------------------
# Signing (FVSign.java)
# ---------------------------------------------------------------------------


def dual_hash(data: bytes) -> bytes:
    """Return the 8-byte AJiaSu "dual hash" of *data*.

    Two independent 32-bit accumulators are updated for every byte::

        h2 = (h2 ^ ((c + 32*h2 + (h2 >> 2)) & 0xFFFFFFFF)) & 0xFFFFFFFF
        h1 = (c + 65599*h1) & 0xFFFFFFFF

    seeded with ``h1 = 0xCAFEBABE`` and ``h2 = 0xDEADBEEF``.  The Java loop
    processes bytes in pairs but performs the same per-byte update, so pairing
    is only an unrolling optimisation.  Output bytes are interleaved::

        h2>>24, h1>>24, h2>>16, h1>>16, h2>>8, h1>>8, h2, h1
    """
    h1 = 0xCAFEBABE
    h2 = 0xDEADBEEF
    for c in data:
        h2 = (h2 ^ ((c + 32 * h2 + (h2 >> 2)) & 0xFFFFFFFF)) & 0xFFFFFFFF
        h1 = (c + 65599 * h1) & 0xFFFFFFFF
    return bytes(
        (
            (h2 >> 24) & 0xFF,
            (h1 >> 24) & 0xFF,
            (h2 >> 16) & 0xFF,
            (h1 >> 16) & 0xFF,
            (h2 >> 8) & 0xFF,
            (h1 >> 8) & 0xFF,
            h2 & 0xFF,
            h1 & 0xFF,
        )
    )


def url_encode(value: str) -> str:
    """Percent-encode *value* the way FVSign.urlEncode does.

    Only ``A-Z a-z 0-9 - . _ ~`` are literal; every other UTF-8 byte becomes
    ``%xx`` with lowercase hex.  A space becomes ``%20``, never ``+``.
    """
    out = []
    for byte in value.encode("utf-8"):
        if byte in _UNRESERVED:
            out.append(chr(byte))
        else:
            out.append("%%%02x" % byte)
    return "".join(out)


def build_query(params: Mapping[str, str]) -> str:
    """Serialize *params* as a signed request body.

    Keys are sorted (Java ``TreeMap`` order), keys are emitted verbatim, only
    values are percent-encoded, and pairs are joined with ``&``.  There is no
    leading or trailing separator.
    """
    parts = []
    for key in sorted(params):
        parts.append(key + "=" + url_encode(params[key]))
    return "&".join(parts)


def sign(secret: str, body: str, timestamp: str) -> str:
    """Return the lowercase hex ``X-Sig`` value.

    The signed byte string is ``secret|body|timestamp|secret`` encoded as
    UTF-8, hashed with :func:`dual_hash` and hex encoded.
    """
    material = secret + "|" + body + "|" + timestamp + "|" + secret
    return dual_hash(material.encode("utf-8")).hex()


# ---------------------------------------------------------------------------
# AES-CTR (FvCrypto.aesCtr)
# ---------------------------------------------------------------------------


def aes_ctr(key: bytes, iv: bytes, data: bytes) -> bytes:
    """AES-CTR with no padding.  Encryption and decryption are identical."""
    if len(key) not in (16, 24, 32):
        raise ValueError("AES key must be 16, 24 or 32 bytes, got %d" % len(key))
    if len(iv) != 16:
        raise ValueError("AES-CTR IV must be 16 bytes, got %d" % len(iv))
    encryptor = Cipher(algorithms.AES(key), modes.CTR(iv)).encryptor()
    return encryptor.update(data) + encryptor.finalize()


# ---------------------------------------------------------------------------
# RSA-OAEP (FvCrypto.rsaOaepEncrypt)
# ---------------------------------------------------------------------------


def _hash_algorithm(hash_name: str) -> hashes.HashAlgorithm:
    try:
        return _HASHES[hash_name]()
    except KeyError as exc:
        raise ValueError(
            "unsupported OAEP hash %r (expected one of %s)"
            % (hash_name, ", ".join(_HASHES))
        ) from exc


def _rsa_oaep_encrypt_with_key(
    public_key: rsa.RSAPublicKey, data: bytes, hash_name: str = "SHA-1"
) -> bytes:
    """OAEP encrypt with MGF1 using the same hash as the OAEP digest."""
    algorithm = _hash_algorithm(hash_name)
    return public_key.encrypt(
        data,
        padding.OAEP(
            mgf=padding.MGF1(algorithm=algorithm),
            algorithm=algorithm,
            label=None,
        ),
    )


def _server_public_key() -> rsa.RSAPublicKey:
    return rsa.RSAPublicNumbers(FV_RSA_EXPONENT, FV_RSA_MODULUS).public_key()


def rsa_oaep_encrypt(session_key: bytes, hash_name: str = "SHA-1") -> bytes:
    """Encrypt a 16-byte FV session key under the embedded server key.

    Returns the raw 128-byte RSA ciphertext (1024-bit modulus).  SHA-1 and
    MGF1-SHA-1 is the OpenAJS default; whether the live server accepts it is
    [INFERENCE].
    """
    if len(session_key) != 16:
        raise ValueError("FV session key must be 16 bytes, got %d" % len(session_key))
    return _rsa_oaep_encrypt_with_key(_server_public_key(), session_key, hash_name)


def oaep_block(session_key: bytes, hash_name: str = "SHA-1") -> bytes:
    """Return the ``k=`` payload: ``b"OAEP" + RSA-OAEP(session_key)``."""
    return OAEP_BLOCK_MAGIC + rsa_oaep_encrypt(session_key, hash_name)


# ---------------------------------------------------------------------------
# FV framing
# ---------------------------------------------------------------------------


def build_auth_body(fields: Mapping[str, str]) -> bytes:
    """Serialize FV key/value fields as ``key=value\\n`` in insertion order.

    Newlines inside values are replaced by spaces, matching
    FvConnection.buildBody.  ``=`` inside values is preserved; parsers split on
    the first ``=`` only.
    """
    out = []
    for key, value in fields.items():
        out.append(key + "=" + value.replace("\n", " ") + "\n")
    return "".join(out).encode("utf-8")


def build_frame(header_iv: bytes, text: str) -> bytes:
    """Build a client-to-server FV frame.

    Wire layout::

        16 bytes  header IV, cleartext
        n bytes   AES-CTR(header_iv, IV=0) over:
                    00 00 <text_len:uint16 BE> <text UTF-8>

    The 2 reserved bytes are zero.  ``text_len`` is the UTF-8 byte length.
    """
    if len(header_iv) != 16:
        raise ValueError("header IV must be 16 bytes, got %d" % len(header_iv))
    text_bytes = text.encode("utf-8")
    if len(text_bytes) > MAX_FRAME_TEXT:
        raise ValueError(
            "frame text is %d bytes; the 16-bit length field allows at most %d"
            % (len(text_bytes), MAX_FRAME_TEXT)
        )
    plain = b"\x00\x00" + len(text_bytes).to_bytes(2, "big") + text_bytes
    return header_iv + aes_ctr(header_iv, b"\x00" * 16, plain)


def _parse_lines(text: str) -> dict[str, str]:
    fields: dict[str, str] = {}
    for line in text.split("\n"):
        eq = line.find("=")
        if eq > 0:
            fields[line[:eq]] = line[eq + 1 :]
    return fields


def parse_response(
    response: bytes, header_iv: bytes, session_key: bytes, body_iv: bytes
) -> dict[str, str]:
    """Decode an FV response frame and return its key/value fields.

    Server-to-client frames normally begin directly with the encrypted 4-byte
    header (no IV prefix) and reuse the client's ``header_iv`` at CTR position
    0.  For convenience, if *response* starts with *header_iv*, that 16-byte
    client-style prefix is stripped first, so a frame from :func:`build_frame`
    can be parsed by the same function.

    The header text is parsed as ``key=value`` lines; the encrypted body that
    follows it is parsed the same way.  Body values win on duplicate keys.
    """
    if len(header_iv) != 16:
        raise ValueError("header IV must be 16 bytes, got %d" % len(header_iv))
    payload = response
    if len(payload) >= 16 and payload[:16] == header_iv:
        payload = payload[16:]
    if len(payload) < 4:
        return {}

    header_plain = aes_ctr(header_iv, b"\x00" * 16, payload[:4])
    text_len = int.from_bytes(header_plain[2:4], "big")
    body_start = 4 + text_len
    # CTR is a stream: decrypt header and text in one call, then drop the
    # 4-byte header.  Decrypting only the first 4 bytes would leave the text
    # ciphertext untouched.
    text_plain = aes_ctr(header_iv, b"\x00" * 16, payload[:body_start])
    text = text_plain[4:].decode("utf-8", "replace")
    fields = _parse_lines(text)

    if body_start < len(payload):
        body_plain = aes_ctr(session_key, body_iv, payload[body_start:])
        fields.update(_parse_lines(body_plain.decode("utf-8", "replace")))
    return fields


# ---------------------------------------------------------------------------
# SOCKS5 request carried inside the encrypted data channel
# ---------------------------------------------------------------------------


def socks5_connect_request(host: str, port: int) -> bytes:
    """Return the SOCKS5 CONNECT request OpenAJS sends (ATYP=3, domain)."""
    host_bytes = host.encode("utf-8")
    if len(host_bytes) > 255:
        raise ValueError("SOCKS5 domain name is limited to 255 bytes")
    if not 0 <= port <= 0xFFFF:
        raise ValueError("port out of range: %d" % port)
    return (
        bytes((5, 1, 0, 3, len(host_bytes)))
        + host_bytes
        + port.to_bytes(2, "big")
    )


# ---------------------------------------------------------------------------
# Deterministic demo
# ---------------------------------------------------------------------------


def main() -> int:
    """Print deterministic vectors; performs no network I/O."""
    secret = OPEN_AJS_SECRET
    body = (
        "_site=ajs&_v=4.11.10.0&cmd=ClientApiLogin%2FLogin"
        "&password=p%40ss&trigger=login&username=test%40example.com"
    )
    timestamp = "1761177600000"

    print("dual_hash('') =", dual_hash(b"").hex())
    print("dual_hash('abc') =", dual_hash(b"abc").hex())
    print("sign =", sign(secret, body, timestamp))
    print(
        "build_query =",
        build_query(
            {
                "_os_dim": "w=1080&h=2400&d=440",
                "_site": "ajs",
                "cmd": "ClientApiLogin/Login",
            }
        ),
    )

    nist_key = bytes.fromhex("2b7e151628aed2a6abf7158809cf4f3c")
    nist_iv = bytes.fromhex("f0f1f2f3f4f5f6f7f8f9fafbfcfdfeff")
    nist_plain = bytes.fromhex(
        "6bc1bee22e409f96e93d7e117393172a"
        "ae2d8a571e03ac9c9eb76fac45af8e51"
        "30c81c46a35ce411e5fbc1191a0a52ef"
        "f69f2445df4f9b17ad2b417be66c3710"
    )
    print("aes_ctr(NIST) =", aes_ctr(nist_key, nist_iv, nist_plain).hex())

    frame = build_frame(bytes(range(16)), "s=\nn=" + "00" * 16 + "\nk=\n_=abc")
    print("frame =", frame.hex())

    session_key = bytes.fromhex("00112233445566778899aabbccddeeff")
    ciphertext = rsa_oaep_encrypt(session_key)
    print("rsa_oaep_encrypt length =", len(ciphertext))
    print("oaep_block length =", len(oaep_block(session_key)))
    print("socks5 =", socks5_connect_request("example.com", 443).hex())
    print("base64(k) length =", len(base64.b64encode(oaep_block(session_key))))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
