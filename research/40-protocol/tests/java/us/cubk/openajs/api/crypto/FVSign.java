package us.cubk.openajs.api.crypto;

import java.nio.charset.StandardCharsets;
import java.util.Map;
import java.util.TreeMap;

public final class FVSign {

    private FVSign() {
    }

    public static String urlEncode(String value) {
        byte[] bytes = value.getBytes(StandardCharsets.UTF_8);
        StringBuilder sb = new StringBuilder(bytes.length + 8);
        for (byte raw : bytes) {
            int c = raw & 0xFF;
            boolean unreserved = (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~';
            if (unreserved) {
                sb.append((char) c);
            } else {
                sb.append('%').append(String.format("%02x", c));
            }
        }
        return sb.toString();
    }

    public static String buildQuery(Map<String, String> params) {
        TreeMap<String, String> sorted = new TreeMap<>(params);
        StringBuilder sb = new StringBuilder();
        for (Map.Entry<String, String> entry : sorted.entrySet()) {
            if (sb.length() > 0) {
                sb.append('&');
            }
            sb.append(entry.getKey()).append('=').append(urlEncode(entry.getValue()));
        }
        return sb.toString();
    }

    public static byte[] dualHash(byte[] data) {
        long h1 = 0xCAFEBABEL;
        long h2 = 0xDEADBEEFL;
        int n = data.length;
        int pairs = n & ~1;
        int i = 0;
        while (i < pairs) {
            int c0 = data[i] & 0xFF;
            h2 = (h2 ^ ((c0 + 32 * h2 + (h2 >> 2)) & 0xFFFFFFFFL)) & 0xFFFFFFFFL;
            h1 = (c0 + 65599 * h1) & 0xFFFFFFFFL;
            int c1 = data[i + 1] & 0xFF;
            h2 = (h2 ^ ((c1 + 32 * h2 + (h2 >> 2)) & 0xFFFFFFFFL)) & 0xFFFFFFFFL;
            h1 = (c1 + 65599 * h1) & 0xFFFFFFFFL;
            i += 2;
        }
        if ((n & 1) != 0) {
            int c = data[n - 1] & 0xFF;
            h2 = (h2 ^ ((c + 32 * h2 + (h2 >> 2)) & 0xFFFFFFFFL)) & 0xFFFFFFFFL;
            h1 = (c + 65599 * h1) & 0xFFFFFFFFL;
        }
        return new byte[]{(byte) (h2 >> 24), (byte) (h1 >> 24), (byte) (h2 >> 16), (byte) (h1 >> 16), (byte) (h2 >> 8), (byte) (h1 >> 8), (byte) h2, (byte) h1};
    }

    public static String hex(byte[] data) {
        StringBuilder sb = new StringBuilder(data.length * 2);
        for (byte b : data) {
            sb.append(String.format("%02x", b & 0xFF));
        }
        return sb.toString();
    }

    public static String sign(String secret, String body, String timestamp) {
        String input = secret + "|" + body + "|" + timestamp + "|" + secret;
        return hex(dualHash(input.getBytes(StandardCharsets.UTF_8)));
    }
}
