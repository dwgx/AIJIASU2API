# TAP driver bundle — identity, signing, install mechanism

Conclusion up front: **`tap0901.sys` is the stock OpenVPN TAP-Windows NDIS 6 virtual adapter
driver, shipped unmodified.** AJiaSu does not ship a proprietary kernel driver. Everything in
`tap/` is third-party GPLv2 code plus Microsoft's DevCon tool.

All paths below are relative to `C:/Users/dwgx1/AppData/Local/Temp/ajs-win-20261023/`.
Nothing here was installed or executed.

## 1. Identity

| Evidence | Value |
|---|---|
| INF banner | `Copyright (C) 2002-2014 OpenVPN Technologies, Inc.` … `GNU General Public License version 2` |
| INF developer note | `If you are bundling the TAP-Windows driver with your app, you should try to rename it in such a way that it will not collide with other instances of TAP-Windows…` — **not** followed: the driver keeps the stock `tap0901` name |
| `DeviceDescription` | `TAP-Windows Adapter V9` |
| `Provider` | `TAP-Windows Provider V9` |
| Service / device id | `tap0901` |
| `.sys` PDB path | `c:\users\samuli\opt\tap-windows6-mattock\src\amd64\tap0901.pdb` (9.21.2 x64) |
| `.sys` source files | `…\tap-windows6-mattock\src\device.c`, `rxpath.c`, `txpath.c` |
| `.sys` product string | `TAP-Windows Virtual Network Driver (NDIS 6.0)`, `CompanyName = The OpenVPN Project` |
| Imports | `NDIS.SYS`, `ntoskrnl.exe` (amd64/arm64); `+ HAL.dll` on i386/arm64 |
| Device namespace | `\Device\`, `\DosDevices\Global\` (from `.sys` strings) |

`tap-windows6-mattock` is the OpenVPN tap-windows6 fork; the user path `c:\users\samuli` is the
build machine of the upstream maintainer, i.e. the binary is an upstream build, not an AJiaSu build.
`[INFERENCE]`

## 2. The four shipped variants

| Bundle | Arch | INF `DriverVer` | `.sys` FileVersion | `.sys` size | `.sys` embedded signature | Catalog signer |
|---|---|---|---|---|---|---|
| `tap/tap/amd64` | x64 | `04/21/2016,9.00.00.21` | `9.21.2 9/21` | 27,136 | none (`NotSigned`) | `OpenVPN Technologies, Inc.` (DigiCert Assured ID Code Signing CA-1) |
| `tap/tap/i386` | x86 | `04/21/2016,9.00.00.21` | `9.21.2 9/21` | 23,040 | none (`NotSigned`) | `OpenVPN Technologies, Inc.` (DigiCert Assured ID Code Signing CA-1) |
| `tap/tap/arm64` | arm64 | `09/27/2019,9.24.2.601` | `9.24.2 9/24` | 40,128 | WHQL | `Microsoft Windows Hardware Compatibility Publisher` |
| `tap/tap6/amd64` | x64 | `10/08/2020,9.24.6.601` | `9.24.6 9/24` | 39,920 | WHQL | `Microsoft Windows Hardware Compatibility Publisher` |
| `tap/tap6/i386` | x86 | `10/08/2020,9.24.6.601` | `9.24.6 9/24` | 34,816 | WHQL | `Microsoft Windows Hardware Compatibility Publisher` |
| `tap/tap6/arm64` | arm64 | `09/27/2019,9.24.2.601` | `9.24.2 9/24` | 40,128 | WHQL | `Microsoft Windows Hardware Compatibility Publisher` |

`tap/tap6/arm64/{OemVista.inf,tap0901.cat,tap0901.sys,tapinstall.exe}` are byte-identical to
`tap/tap/arm64/*` — verified by SHA256 equality (`fdfd0e01…`, `ead186bd…`, `8417dc50…`, `c24b1c75…`).
So the bundle really contains three driver builds, not four.

### Signing detail

| File | Signer | Cert validity | Timestamp |
|---|---|---|---|
| `tap/tap/amd64/tap0901.cat` | `OpenVPN Technologies, Inc.` | 2013-08-13 → 2016-09-02 | DigiCert Timestamp Responder, 2014-10-22 → 2024-10-22 |
| `tap/tap6/amd64/tap0901.cat` | `Microsoft Windows Hardware Compatibility Publisher` | 2020-03-12 → 2021-03-06 | Microsoft Time-Stamp Service, 2019-11-14 → 2021-02-12 |
| `tap/tap/arm64/tap0901.cat` | `Microsoft Windows Hardware Compatibility Publisher` | 2019-06-06 → 2020-06-04 | Microsoft Time-Stamp Service, 2019-09-07 → 2020-12-05 |
| `tap/tap/amd64/tapinstall.exe` | `OpenVPN Technologies, Inc.` | 2013-08-13 → 2016-09-02 | DigiCert Timestamp Responder |
| `tap/tap6/arm64/tapinstall.exe` | `OpenVPN Inc.` (EV, Delaware) | — | DigiCert EV Code Signing CA (SHA2) |

`Get-AuthenticodeSignature` reports all catalogs and the WHQL drivers as `Valid`; the 9.21.2 `.sys`
files themselves are `NotSigned` because their signature lives only in the catalog — this is normal
for driver packages and does **not** mean unsigned installation.

`[INFERENCE]` Practical consequence: the 9.21.2 pair (amd64/i386) predates Microsoft's 2015+
kernel-mode signing policy and carries no WHQL signature. On a Secure Boot machine the 9.24.x pair
is the one that will install without a policy override. Which pair AJiaSu actually picks at runtime
is `[UNKNOWN]` from static data (see `INVENTORY.md` open question 5).

## 3. INF device IDs and service definition

From `tap/tap/amd64/OemVista.inf` (identical structure in every variant, only the decorated model
section differs — `NTamd64` / `NTx86` / `NTarm64`):

```ini
[Version]
   Signature   = "$Windows NT$"
   CatalogFile = tap0901.cat
   ClassGUID   = {4d36e972-e325-11ce-bfc1-08002be10318}
   Provider    = %Provider%
   Class       = Net

[Manufacturer]
   %Provider% = tap0901, NTamd64

[tap0901.NTamd64]
   %DeviceDescription% = tap0901.ndi, root\tap0901   ; Root enumerated
   %DeviceDescription% = tap0901.ndi, tap0901        ; Legacy
```

Device / hardware IDs: **`root\tap0901`** (root-enumerated, the one DevCon uses) and **`tap0901`**
(legacy). Class GUID `{4d36e972-e325-11ce-bfc1-08002be10318}` = Net class.

```ini
[tap0901.ndi]
   CopyFiles       = tap0901.driver, tap0901.files
   AddReg          = tap0901.reg
   AddReg          = tap0901.params.reg
   *IfType            = 0x6   ; IF_TYPE_ETHERNET_CSMACD
   *MediaType         = 0x0   ; NdisMedium802_3
   *PhysicalMediaType = 14    ; NdisPhysicalMedium802_3

[tap0901.ndi.Services]
   AddService = tap0901, 2, tap0901.service

[tap0901.service]
   DisplayName   = %DeviceDescription%
   ServiceType   = 1    ; SERVICE_KERNEL_DRIVER
   StartType     = 3    ; SERVICE_DEMAND_START
   ErrorControl  = 1
   LoadOrderGroup = NDIS
   ServiceBinary = %12%\tap0901.sys
```

The adapter is therefore presented to Windows as an **Ethernet** NIC, not as a "VPN" or "tunnel"
interface. `[INFERENCE]` That is why AJiaSu can drive it with ordinary IPv4 configuration
(`netsh interface ipv4 set address|set dns|add dns`) and plain route-table edits, with no custom
NDIS OIDs.

Tunable registry parameters exposed by the INF (`HKR, Ndi\params\…`): `MTU` (100–1500, default
1500), `MediaStatus` (0 = application controlled, 1 = always connected), `MAC`, `AllowNonAdmin`
(default 1 = allowed).

No custom IOCTL names appear in the `.sys` strings — the TAP driver uses the well-known TAP-Windows
IOCTL set (`TAP_IOCTL_*`), which is numeric and therefore not visible as strings. `[INFERENCE]`
`[UNKNOWN]` without disassembly of the driver's `IRP_MJ_DEVICE_CONTROL` handler.

## 4. Install mechanism

`tapinstall.exe` is Microsoft **DevCon** rebuilt and signed by OpenVPN. Evidence from its strings:

```
%1 Usage: %1 [-r] [-m:\\<machine>] <command> [<arg>...]
<command>    Specifies a Devcon command (see command list below).
Devcon Install Command
Installs the specified device manually. Valid only on the local computer.
%1 [-r] %2 <inf> <hwid>
Inf file is %1
Inf was installed by using F6 during text mode setup
```

The INF's own header comment documents the intended invocation:

```
;   tapinstall install OemVista.inf tapoas
;   tapinstall update OemVista.inf tapoas
;   tapinstall remove tapoas
```

`AJiaSu.exe` builds the same command at runtime. String cluster at `.rdata` `0x314990`–`0x314ad4`:

```
\arm64\   \amd64\   \i386\   4D36E972-E325-11CE-BFC1-08002BE10318
TAP-Windows Adapter V9   tapinstall.exe   enable    disable    OemVista.inf   install "
```

`[INFERENCE]` Reconstructed command line:
`<install dir>\tap\<tap|tap6>\<arch>\tapinstall.exe install "<…>\OemVista.inf" tap0901`
(the literal `install "` is followed by the INF path; `enable `/`disable ` are used for the
adapter's device node).

Trigger points in the app: `PromptInstallVpnDriver`, `PromptCannotInstallVpnDriver`,
`vpnConnect: can not prepare tun dev`. So driver installation happens **on first connect**, from the
client, under the client's elevated context — not from the NSIS installer (see `installer/NSIS.md`).

## 5. What this means for the project

1. The tunnel is a plain Ethernet-over-TAP arrangement. A replacement client does not need to
   reverse-engineer a kernel driver; it can reuse the same signed TAP-Windows package or any
   equivalent (e.g. Wintun) and speak the FV protocol in user space.
2. Because the driver is stock, the only proprietary knowledge is in `AJiaSu.exe`: the FV handshake,
   the packet obfuscation (`buffer_obscure_packet`, `FV_CipherKeySize`), and the routing/DNS policy.
3. The `tap` vs `tap6` split is a driver-generation compatibility matrix (9.21.2 legacy vs 9.24.6
   WHQL), not a protocol difference.
