import us.cubk.openajs.api.crypto.FVSign;

import java.io.BufferedReader;
import java.io.FileReader;
import java.nio.charset.StandardCharsets;
import java.util.LinkedHashMap;
import java.util.Map;

/**
 * Independent vector generator for the Python reference implementation.
 *
 * FVSign.java next to this file is a verbatim copy from
 * https://github.com/cubk1/OpenAJS (commit cae4308bef4de609a1091b790696290eb5b69e9b),
 * released into the public domain under the Unlicense.
 *
 * Input file: one record per line, tab separated.
 *   S <hex secret> <hex body> <hex timestamp>   -> S <index> <hex dualHash(body)> <sign>
 *   Q <hex key> <hex value> [<hex key> <hex value> ...] -> Q <index> <hex buildQuery(params)>
 *
 * Everything is hex encoded so that the harness never depends on console encoding.
 * Usage: java VectorHarness <input-file>
 */
public final class VectorHarness {

    private VectorHarness() {
    }

    public static void main(String[] args) throws Exception {
        if (args.length != 1) {
            System.err.println("usage: VectorHarness <input-file>");
            System.exit(2);
        }
        int index = 0;
        try (BufferedReader reader = new BufferedReader(new FileReader(args[0], StandardCharsets.UTF_8))) {
            String line;
            while ((line = reader.readLine()) != null) {
                if (line.isEmpty()) {
                    continue;
                }
                String[] parts = line.split("\t", -1);
                if ("S".equals(parts[0])) {
                    String secret = unhex(parts[1]);
                    String body = unhex(parts[2]);
                    String timestamp = unhex(parts[3]);
                    String dual = FVSign.hex(FVSign.dualHash(body.getBytes(StandardCharsets.UTF_8)));
                    String sig = FVSign.sign(secret, body, timestamp);
                    System.out.println("S\t" + index + "\t" + dual + "\t" + sig);
                } else if ("Q".equals(parts[0])) {
                    Map<String, String> params = new LinkedHashMap<>();
                    for (int i = 1; i + 1 < parts.length; i += 2) {
                        params.put(unhex(parts[i]), unhex(parts[i + 1]));
                    }
                    String query = FVSign.buildQuery(params);
                    System.out.println("Q\t" + index + "\t" + FVSign.hex(query.getBytes(StandardCharsets.UTF_8)));
                } else {
                    throw new IllegalArgumentException("unknown record type: " + parts[0]);
                }
                index++;
            }
        }
    }

    private static String unhex(String hex) {
        byte[] out = new byte[hex.length() / 2];
        for (int i = 0; i < out.length; i++) {
            out[i] = (byte) Integer.parseInt(hex.substring(i * 2, i * 2 + 2), 16);
        }
        return new String(out, StandardCharsets.UTF_8);
    }
}
