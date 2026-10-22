# NSIS installer / uninstaller — script findings

Static parse only. The installer was never executed. All offsets are file offsets in
`D:/Project/LovePlusSpeedToProxy/research/AJiaSu_4.9.6.0.exe`.

## 1. Container

| Property | Value | How obtained |
|---|---|---|
| Type | `Nsis`, `NSIS-3 Unicode` | `7z l` |
| Stub | PE32 GUI x86, linker 6.0, `Nullsoft Install System v3.04` | PE header + manifest |
| Manifest | `requestedExecutionLevel level="requireAdministrator"` | embedded manifest |
| Compression | Deflate, **non-solid** | `7z l` (`Method = Deflate`, `Solid = -`) |
| Header size | 19,370 B (compressed to 4,144 B) | `7z l`, own parse |
| Data block | 5,434,749 B physical, tail 3 B | `7z l` |
| Files | 37 | `7z l` |
| Version resource | `FileVersion 4.9.6.0`, `ProductVersion 4.9.6.0`, `FileDescription AJiaSu`, `Comments https://www.91ajs.com`, `CompanyName ___`, `ProductName ___`, `LegalCopyright ___` | `7z l` comment block |
| Authenticode | `Valid`, CN=`上海游盾网络科技有限公司`, GlobalSign GCC R45 EV CodeSigning CA 2020, cert 2024-03-13 → 2027-03-14 | `Get-AuthenticodeSignature` |
| PE timestamp | 2018-12-16 07:24:46 (NSIS stub build date, not the payload date) | `7z l` |

Note the placeholder metadata: `CompanyName`, `ProductName`, `LegalCopyright` are literally `___`.

## 2. Header layout (verified by parsing)

```
0x0FA00  u32  flags              = 0
0x0FA04  u32  siginfo            = 0xDEADBEEF
0x0FA08  u8x12 magic             = "NullsoftInst"
0x0FA14  u32  length_of_header   = 0x4BAA = 19370
0x0FA18  u32  length_of_data     = 0x52ED7D = 5434749
0x0FA1C  u32  block0             = 0x80001030  (bit31 = compressed, size 0x1030 = 4144)
0x0FA20  ...  raw DEFLATE stream -> 19370 B
```

Reproduce:

```python
import zlib
d = open("AJiaSu_4.9.6.0.exe","rb").read()
assert d[0xfa08:0xfa14] == b"NullsoftInst"
hdr = zlib.decompressobj(-15).decompress(d[0xfa20:0xfa20+0x1030])
len(hdr)  # 19370
```

The decompressed header holds the compiled install script (opcode stream + string table). It is not
disassembled to opcodes here; the string table alone identifies every path, registry key and plugin
call the script performs.

## 3. Install script findings (from the 19,370 B header)

### Paths

```
$PROGRAMFILES\AJiaSu                      (ProgramFilesDir + "\AJiaSu")
\AJiaSu.exe   \sciter.dll   \res.fvr   \lang.fvr   \product.dat
\tap\tap\{amd64,arm64,i386}\{OemVista.inf,tap0901.cat,tap0901.sys,tapinstall.exe}
\tap\tap6\{amd64,arm64,i386}\{…same four files…}
\sysproxy\{amd64,arm64,i386}\sysproxy.exe
\uninst.exe
\config.ini
\res.zip  \lang.zip                       (staging names present in the script)
```

`\res.zip` / `\lang.zip` appear next to `res.fvr` / `lang.fvr`; `[INFERENCE]` the script unpacks the
ZIP payloads through these temporary names, or they are leftovers from an older build.

### Registry

`HKLM\Software\Microsoft\Windows\CurrentVersion\Uninstall\AJiaSu`:

| Value | Data |
|---|---|
| `DisplayName` | AJiaSu |
| `DisplayVersion` | `4.9.6.0` |
| `DisplayIcon` | `<install dir>\AJiaSu.exe,0` |
| `InstallLocation` | `<install dir>` |
| `UninstallString` | `"<install dir>\uninst.exe"` |
| `URLInfoAbout` | `https://www.91ajs.com` |
| `HelpLink` | `https://www.91ajs.com` |
| `Publisher` | (product publisher string) |
| `EstimatedSize` | `0x600` (1,536 KB) |
| `NoModify` | 1 |
| `NoRepair` | 1 |

### UI

Standard NSIS wizard (`modern-wizard.bmp`) plus a custom page built with the `InstallOptions`
plugin (`\InstallOptions.dll`, dialog template `\ioSpecial.ini`). The compiled template declares
`NumFields = 5` including a `RadioButton` group (fields 4) and a `CheckBox` (field 5) — consistent
with an install-path/bundle-choice page and an "agree/create shortcut" checkbox. `[INFERENCE]` on the
exact labels; the labels are localised at runtime and are not stored as literals.

`\System.dll` is used only for cosmetic calls — the script's `Call` entries reference
`USER32::SystemParametersInfo(i0x42,…)` and `UXTHEME::SetWindowTheme(p,w" ",w" ")`. There is **no**
`ADVAPI32::CreateService` / `RegCreateKeyEx` style plugin call in the installer script.

### Behaviour

- Creates a shortcut (`.lnk`) to `AJiaSu.exe`.
- Writes `config.ini` (`[CONFIG]` section) into the install directory.
- **Does not install the TAP driver** (with the caveat below). The installer's string table contains
  the four TAP filenames — `OemVista.inf`, `tap0901.cat`, `tap0901.sys`, `tapinstall.exe` — which is
  exactly what `File` copy instructions require. It contains **no** command/argument string:
  `install "` → 0 hits, `remove` → 0, `update` → 0, standalone `tap0901` → 0 (only `tap0901.cat` and
  `tap0901.sys`), `CreateService`/`AddService`/`pnputil`/`netcfg`/`devcon` → 0. The uninstaller has
  `tapinstall`/`tap0901`/`devcon` at 0 hits. The complete invocation (`install "` + `OemVista.inf` +
  standalone `tap0901`) exists only in `AJiaSu.exe`, which runs it on first connect.
  **Caveat:** the NSIS entry stream (opcode + string-index pairs) was **not** decoded, so an
  `ExecWait` on a path built at runtime from those filename strings cannot be excluded by string
  evidence alone. Treat "installer never runs tapinstall" as WEAKENED, not proven.
- **Does not register a Windows service** and does not schedule a task; auto-start is configured by
  the app via Task Scheduler (`ITaskService`, `AutoLaunch`).
- Requires elevation (`requireAdministrator`) purely to write `%ProgramFiles%` and `HKLM`.

## 4. Uninstaller

`uninst.exe.nsis` (20,742 B on disk, extracted by 7-Zip) is the compiled uninstaller script block,
not text:

```
0x00 u32 1                  (header version)
0x04 u32 0xDEADBEEF
0x08 u8x12 "NullsoftInst"
0x14 u32 0x3FF0 = 16368     (decompressed size)
0x18 u32 0x5106 = 20742     (file size)
0x1C u32 0x80000CDF         (compressed, 0x0CDF = 3295 B)
0x20 ... raw DEFLATE -> 16368 B
```

Decompressed string table:

```
Software\Microsoft\Windows\CurrentVersion\Uninstall\AJiaSu
Software\AJiaSu
\tap        \tapwin        \sysproxy
\res.fvr    \lang.fvr      \product.dat
\config.ini \AJiaSu.exe    \sciter.dll   \uninst.exe
\Un_<random>.exe            (NSIS self-copy for delete-on-reboot)
\wininit.ini
Nullsoft Install System v3.04
4.9.6.0
```

Behaviour: removes the file set above, deletes the `Uninstall\AJiaSu` and `Software\AJiaSu` keys, and
uses the NSIS `\Un_*.exe` + `wininit.ini` trick when it cannot delete itself immediately.

`\tapwin` is deleted but **does not exist** in the 4.9.6.0 payload — `[INFERENCE]` a legacy
directory name from an older build.

## 5. Bundled prerequisites

None. The installer ships no VC++ redistributable, no .NET runtime, no WebView2, no driver package
installer. `AJiaSu.exe` is statically linked (libcurl, mbedTLS, lwIP, zlib), so no CRT DLL is
required either. The only prerequisites are OS components: `WININET`, `RASAPI32`, `IPHLPAPI`,
`SETUPAPI`, `bcrypt`, and Task Scheduler 2.0 (`ITaskService`).
