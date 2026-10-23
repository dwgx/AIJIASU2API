# AJS-84 — Android client 4.12.2.0 static RE: control plane, native core, dashboard surface

Status: static, read-only. 2026-10-24. Slice `AJS-84/APK`. No APK execution, no vendor
contact, no credentials. This file is the only artifact written by this slice.
Scope: `AJiaSu_4.12.2.0_ajs-official-website.apk` (sha256 `42ce2b51…df3cb`), decompiled with
jadx 1.5.6 into scratch, manifest decoded with apktool 3.0.3, native core inspected with
rabin2/radare2 6.2.2. All scratch paths are under
`C:/Users/dwgx1/.dwgx/tmp/ajs-re-20261024a/` (`jadx-out/`, `apktool-out/`, `sig/`).
Everything below is anchored to `class:line` in the decompiled tree or `file offset` in
`libfvcore.so`. `[UNVERIFIED]` marks what this slice did not execute or read.

## 1. Bottom line

1. **The Android control plane is one endpoint, not many.** Every vendor API call is
   `POST <base>/client.php` with `cmd=<ClientApi…>`; the Android layer holds no API host list
   except the four failover bases in `ju0.f8355c` (`p000/ju0.java:37`). Multi-host rotation is
   inside native (`FVNetClient::saveFetchedUrlHosts`, `FVHttpClient::addUrlHosts`).
2. **The dashboard's whole model comes from one response.** `ClientApiLogin/Login` returns
   `LoginSync`, a ~130-field JSON that already carries the server tree, per-node ports, per-node
   ping secret, marks, membership, quota, feature flags and update info. Android parses it in
   `p000/C0023am.java:184-425`; the parser is a complete, readable schema (section 9).
3. **Connect modes are three, and "auto" is a UDP reachability test, not a latency race.**
   `auto` picks the UDP/VPN implementation only if the node's UDP port is set *and* the last ping
   report has a non-negative UDP result (`p000/C0060bl.java:381-390`).
4. **The tunnel handshake key has not rotated.** The client's embedded RSA modulus
   (`p000/RunnableC1787y7.java:423`, 1024-bit, e=65537) is byte-identical to
   `emulator/internal/fv/crypto.go:37` and `research/40-protocol/PROTOCOL.md:649`, i.e. the same
   key as the Linux 4.2.3.0 build. A cross-platform confirmation of the existing emulator
   assumption, not a new key.
5. **The 4.12 client is more talkative than 4.2.3.0 in three places that matter for a dashboard
   clone:** a `/client.php` settings-sync command (`ClientApiClientSetting/ClientSetting`) that
   reports local UI settings to the vendor, a `ClientApiReport/Report` payload containing the
   full installed-app list with signatures, and a `ClientApiReport/WebsiteCheckReport`
   censorship probe. None are needed to render a dashboard; section 13 lists what not to
   replicate.
6. **The Android UI is a WebView shell over the same web account pages the panel already
   knows** (`/User/Profile`, `/User/MyOrders`, `/purchase`, …), so account pages can be reused
   as-is; only the tunnel/state layer is app-specific.

## 2. Sample and build identity

| Item | Value | Anchor |
|---|---|---|
| File | `AJiaSu_4.12.2.0_ajs-official-website.apk` | `sha256sum` |
| sha256 | `42ce2b5152c767c3f7412d65793aa15c294e539fb1f499428103205a352df3cb` | verified, matches brief |
| Package | `com.fvcorp.android.aijiasuclient` | `AndroidManifest.xml:2` |
| versionName | `4.12.2.0` | `apktool-out/apktool.yml`, `FVApp.java:143` |
| versionCode | `134` | `apktool-out/apktool.yml` |
| minSdk / target / compile | `23` / `36` / `36` | `apktool.yml`, `AndroidManifest.xml:2` |
| Application class | `com.fvcorp.android.fvclient.FVApp` | `AndroidManifest.xml:47` |
| Code size | one `classes.dex`, 4 646 368 bytes, 1834 classes | `unzip -l`, jadx progress |
| Native ABIs | `arm64-v8a`, `armeabi-v7a`, `x86`, `x86_64` | `apk/lib/` |
| Native core | `libfvcore.so`, 3 429 688 B (arm64), ELF64 aarch64, stripped, PIC, NX, full RELRO | `rabin2 -I` |
| Signing (v1) | `META-INF/CERT.SF` + `META-INF/CERT.RSA` | `unzip -l` |
| Signing (block) | APK Signing Block present, magic `APK Sig Block 42` at file offset `0x124b305`; v2-vs-v3 split `[UNVERIFIED]` (no `apksigner` in the station toolset) | byte search |
| Signer | `CN=Shawn, OU=F, O=F, L=LA, ST=LA, C=US`, self-signed, SHA256withRSA, 2048-bit, valid 2017-12-15 → 2117-11-21, serial `372744e4` | `keytool -printcert -file sig/META-INF/CERT.RSA` |
| Signer SHA-256 | `9A:15:1F:62:72:17:FE:7D:AE:99:9B:D0:29:ED:61:06:39:06:DB:00:AF:36:6F:C1:02:CB:DB:81:A6:2F:29:85` | same |
| APK ZIP comment | empty → channel/feature-flag block absent in this official build (section 8) | `zipfile.ZipFile(...).comment == b''` |

Build hygiene: no `r8-map` file shipped, but the R8 map id leaked into the dex as a per-class
comment, `r8-map-id-0582629be383ed15f5055b6fdb7caba6e8590cd378cadb87075dee793a711355`
(`FVNetClient.java:15`). The obfuscator kept `com.fvcorp.android.fvcore.*` and
`com.fvcorp.android.fvclient.*` intact and pushed ~1937 helper classes into `p000`.

## 3. Manifest

Decoded: `apktool-out/AndroidManifest.xml`. Counts: 16 `uses-permission`, 29 `activity`
(+4 `activity-alias`), 3 `service`, 3 `receiver`, 7 `provider`.

### 3.1 Permissions

| Permission | Note |
|---|---|
| `INTERNET`, `ACCESS_NETWORK_STATE`, `ACCESS_WIFI_STATE`, `CHANGE_WIFI_STATE` | transport + SSID read for the report payload |
| `FOREGROUND_SERVICE`, `FOREGROUND_SERVICE_SYSTEM_EXEMPTED` | VPN foreground service |
| `POST_NOTIFICATIONS` | connection notification (`channel_id_vpn_state`) |
| `RECEIVE_BOOT_COMPLETED`, `RECEIVE_USER_PRESENT` | re-arm widget/notifications |
| `REQUEST_INSTALL_PACKAGES` | in-app APK update (section 12) |
| `QUERY_ALL_PACKAGES` | per-app split-tunnelling picker |
| `HIDE_OVERLAY_WINDOWS` | anti-tapjacking on dialogs |
| `WRITE_EXTERNAL_STORAGE` (maxSdk 23) | legacy download path |
| `com.google.android.gms.permission.AD_ID` | Umeng/Mob attribution |
| `com.android.vending.BILLING` | Play Billing 9.1.0 (`billing.properties`) |
| `…aijiasuclient.DYNAMIC_RECEIVER_NOT_EXPORTED_PERMISSION` | self-defined, `signature` |
| declared `permission` | same name, `protectionLevel="signature"` |

No `ACCESS_FINE_LOCATION`, no `READ_PHONE_STATE`, no `CAMERA`, no `RECORD_AUDIO`, no
`SYSTEM_ALERT_WINDOW`. The `<uses-feature>` block marks camera/nfc/gps/microphone/sensor
`required="false"` — the client does not ask for them.

### 3.2 Components that matter

| Component | Type | Detail |
|---|---|---|
| `…fvclient.vpn.FVService` | service | `android:permission="android.permission.BIND_VPN_SERVICE"`, `foregroundServiceType="systemExempted"`, `android:process=":VpnService"`, intent-filter `android.net.VpnService` — the only tunnel entry point |
| `…fvclient.activity.SplashActivity` | activity | launcher + `LEANBACK_LAUNCHER` |
| `…fvclient.activity.MainActivity` | activity | `singleTask`; hosts all main fragments |
| `…fvclient.activity.LoginActivity` | activity | also handles action `com.fvcorp.LoginActivity.DoRegister` (`LoginActivity.java:101`) |
| `…fvclient.activity.UpdateActivity` | activity | transparent; APK downloader (section 12) |
| `…fvclient.activity.WebViewActivity` | activity | generic web page host |
| `…fvclient.activity.PaymentResultSubmitActivity` | activity | POSTs `result` to a vendor-supplied URL (`PaymentResultSubmitActivity.java:83-85`) |
| `…fvclient.activity.GoogleBillingResultSubmitActivity` | activity | POSTs `purchaseData` + `purchaseSignature` (`GoogleBillingResultSubmitActivity.java:139-142`) |
| `…fvclient.widget.FVAppWidgetProvider` | receiver | `APPWIDGET_UPDATE`; widget shows connect state |
| `…fvclient.sphelper.SPContentProvider` | provider | `authorities=…aijiasuclient.sphelper`, **not exported**; URI grammar `content://…sphelper/{contain,clean}/<prefsFile>[/<key>]` (`SPContentProvider.java:20,41`), used cross-process by `:VpnService` |
| `…wxapi.WXPayEntryActivity`, `…kwai.KwaiHandlerActivity`, `…KwaiRouterActivity` | activity/alias | share/pay SDK entry points (`tencent1106797988`, `kwaisdk://router`) |
| `cn.fly.FlyProvider`, `com.mob.MobProvider`, `cn.sharesdk…ShareSDKFileProvider`, `com.sina.weibo.sdk.content.FileProvider` | providers | Mob/ShareSDK/Weibo share plumbing |
| `com.google.android.datatransport.*` | service/receiver | Firebase CCT telemetry |

### 3.3 Application flags worth flagging

- `android:usesCleartextTraffic="true"` — the app will speak plain HTTP. No cleartext host was
  observed in static analysis; the bases are all `https://` (section 4).
- `android:allowBackup="false"`, `android:fullBackupContent="true"`,
  `android:requestLegacyExternalStorage="true"`, `android:extractNativeLibs="false"`.
- `android:appComponentFactory="androidx.core.app.CoreComponentFactory"` (AndroidX).
- Third-party SDK metadata baked in: `Mob-AppKey`, `KWAI_APP_ID`/`APP_ID`
  `ks690176644135941902`, `com.google.android.play.billingclient.version=9.1.0`.
- `assets/ShareSDK.xml` carries hardcoded WeChat/QQ/Weibo/Kuaishou share app ids and secrets.
  **Values deliberately not reproduced here** (third-party app secrets, not vendor API
  credentials); the file exists at `apk/assets/ShareSDK.xml` in the extraction tree.

## 4. Control-plane hosts and endpoints

### 4.1 Hosts

| # | Host | Role | Anchor |
|---|---|---|---|
| H1 | `https://login.ajs-app.com/` | control base, failover slot 1 | `p000/ju0.java:37` |
| H2 | `https://login.mmegg.com/` | control base, slot 2 | `p000/ju0.java:37` |
| H3 | `https://login.mmegg.com:4443/` | control base, slot 3 (alt port) | `p000/ju0.java:37` |
| H4 | `https://login.mbclubsh.com/` | control base, slot 4 | `p000/ju0.java:37` |
| H5 | `https://www.91ajs.com` | web/account origin default | `p000/uu0.java:371` |
| H6 | `https://m.91ajs.com` | mobile web origin (derived `www.` → `m.`) | `p000/uu0.java:380` |
| H7 | `https://play.google.com/store/apps/details?id=com.fvcorp.android.aijiasuclient` | Play listing used when installer is `com.android.vending` | `UpdateActivity.java:320` |
| H8 | `https://api.weibo.com/oauth2/default.html` | Weibo share redirect (third-party) | `apk/assets/ShareSDK.xml` |

The four bases are passed to native as one `\n`-joined string and are also retried in-app;
`FVNetClient.appInit(workDir, "ajsConfig", joinedUrls, params)` (`FVNetClient.java:329-334`).
Native owns per-host bookkeeping (`FVNetClient::saveFetchedUrlHosts` @ `0x175b78`,
`FVHttpClient::addUrlHosts` @ `0x166008`).

### 4.2 `/client.php` commands

All are `POST <base>/client.php`, body `application/x-www-form-urlencoded`, built by
`jn0.m7426g(Map)` (`p000/jn0.java`, `URLEncoder.encode` per key/value, `&`-joined). Common
params on nearly every call: the `uu0.f11463H` fingerprint block (section 7) plus
`_client_login=<LoginToken>` and `username=<UserName>` where the call is authenticated.

| # | `cmd` | Caller | Extra params | Native? |
|---|---|---|---|---|
| E1 | `/client.php` (the transport itself) | `FVNetClient.java:415`, native `0xd0332` | — | yes |
| E2 | `ClientApiLogin/Login` | `LoginActivity.java:76` → `appApiLoginSync(…, "login", null)` | `trigger=login`; credentials supplied separately via `appSetLoginInfo` | native only `0xc3ebf` |
| E3 | `ClientApiClientSetting/ClientSetting` | `FVNetClient.java:397-415` | `ClientSettings[SetType,Protocol,Reconnect,NetworkLock,Datetime,AdvancedRouteItem,AdvancedRouteContent,SpecifiedApps,SpecifiedAppsShowOnHome,SpecifiedAppsContent,CustomDns,AllowRating,AllowMatchDialog]` | no |
| E4 | `ClientApiClientSetting/LoadRoute` | native only | route/network list download for split tunnelling | native `0xbaa9d` |
| E5 | `ClientApiVpnServer/AutoMatch` | `C0011aa.java:100`, `C0049ba.java:149` | `LocationLevel`, `LocationCode`, `username`, `_client_login` | no |
| E6 | `ClientApiUser/VpnServerFavorite` | `nd0.java:429`, `od0.java:390` | `Action=favorite\|unfavorite`, `ServerId`, `username`, `_client_login` | no |
| E7 | `ClientApiUser/UserVpnServerFavoriteList` | `ServerGroupsFragment.java:604`, `ServersLevelOneFragment.java:158` | `username`, `_client_login` | no |
| E8 | `ClientApiMessageCenter/GetAnnounce` | `MainActivity.java:230` | `username`, `_client_login` → `{Error, ErrorMessage, Content}` | no |
| E9 | `ClientApiMessageCenter/CloseAnnounce` | `HomeFragment.java:677` | `username`, `_client_login` | no |
| E10 | `ClientApiMessageCenter/GetNotificationList` | `MessageCenterFragment.java:166`, `C1592sz.java:37` | `username`, `_client_login` | no |
| E11 | `ClientApiMessageCenter/GetUnreadNotificationCount` | `MainActivity.java:222` | `username`, `_client_login` → `{Result, Count}` | no |
| E12 | `ClientApiRegister/Register` | `RegisterFragment.java:374` | registration form | no |
| E13 | `ClientApiVerifyCode/Captcha` | `RegisterFragment.java:404`, `ResetPasswordFragment.java:369` | captcha image fetch | no |
| E14 | `ClientApiVerifyCode/SendVerifyCode` | `RegisterFragment.java:535`, `ResetPasswordFragment.java:472` | phone/email verify code | no |
| E15 | `ClientApiUser/ResetPassword` | `ResetPasswordFragment.java:414` | reset form | no |
| E16 | `ClientApiUpgrade/UpgradeRecord` | `MainActivity.java:642` | `UpgradeType`, `PriorVersion`, `UpgradeDatetime`, `username`, optional `PopupInterval` | no |
| E17 | `ClientApiReport/Report` | `C1689vk.java:128` | `reportActionType=login\|resume\|connect\|disconnect\|switchNetwork`, `installTime`, `networkData`, `deviceData`, `appData` | no |
| E18 | `ClientApiReport/WebsiteCheckReport` | native only | `url`, `responseTime`, `checkResult`, `remoteIp`, `remotePort`, `httpCode`, `keyword` | native `0xc901b` |
| E19 | `/Payment/NotifyGooglePlayBilling` | `GoogleBillingResultSubmitActivity.java:142` | `username`, `purchaseData`, `purchaseSignature` | no |

`E19` is the one absolute path that is *not* `/client.php` — it is posted verbatim as the URL
argument of `appHttpRequestParams`. `PaymentResultSubmitActivity.java:83-85` posts
`result=<json>` to whatever URL the payment page handed back (`c1493qa.f10239b`).

### 4.3 Account/web routes (relative to H5/H6, joined by `jn0.m7420a`)

| # | Path | Used for | Anchor |
|---|---|---|---|
| W1 | `purchase` | in-app purchase page | `uu0.java:383` |
| W2 | `privacy` | privacy policy | `uu0.java:384` |
| W3 | `User/Feedback` | feedback (built, value discarded) | `uu0.java:385` |
| W4 | `help` | help center | `uu0.java:386` |
| W5 | `help?id=cannot-click-ok` | VPN permission troubleshooting | `uu0.java:387` |
| W6 | `help?id=connection-authorization-failed` | auth-failure troubleshooting | `uu0.java:388` |
| W7 | `Payment/PackageIntroduce` | plan matrix (value discarded) | `uu0.java:389` |
| W8 | `User/Profile` | account page | `uu0.java:390` |
| W9 | `User/ChangePassword` | change password | `uu0.java:391` |
| W10 | `User/ChangeEmail` | change email | `uu0.java:392` |
| W11 | `User/ChangePhoneNumber` | change phone | `uu0.java:393` |
| W12 | `terms` | terms | `uu0.java:394` |
| W13 | `Page/Contact` | contact page | `uu0.java:395` |
| W14 | `agreement` | user agreement | `uu0.java:396` |
| W15 | `User/MyOrders` | order history | `uu0.java:397` |
| W16 | `User/UsageRecords?_uid=<appClientUniqueId()>` | usage records, keyed by native client id | `uu0.java:398`, `MineFragment.java:207` |
| W17 | `anti-fraud` | anti-fraud notice | `uu0.java:399` |
| W18 | `User/KycVerify` | identity verification | `uu0.java:400` |
| W19 | `Page/PreventDisconnect?manufacturer=<Build.MANUFACTURER>` | OEM battery-kill guide | `uu0.java:401` |

`UrlWeb` and `UrlMobile` are *runtime-overridable*: `LoginSync.Login.UrlWeb` and
`LoginSync.MobileUrl.UrlMobile` are read on every login and persisted
(`C0023am.java:202-210`, `FVNetClient.java:218-219`), and `CustomLoginUrl` can replace the whole
base (`FVApp.java:226-231`, `uu0.java:373-377`). Any host list in a dashboard must therefore be
treated as data, not as a constant.

## 5. WebView surface: routes and JS bridge

The account/purchase UI is a `WebView` that calls into the app through a JS bridge with
`cmd=<route>`. Handler entry points:

- `AppWebViewFragment.mo4223a0(cmd, route, params)` — `AppWebViewFragment.java:279-303`
- `BaseMainWebFragment.mo4223a0` — `BaseMainWebFragment.java:27-73`
- URL-override handler keyed on path — `BaseMainWebFragment.java:87-123`

| Bridge route | Effect | Anchor |
|---|---|---|
| `/promptPopup` | native dialog from `Title` + `Content` | `AppWebViewFragment.java:281` |
| `/promptSlide` | toast from `Content` | `AppWebViewFragment.java:293` |
| `/goToHelp` | switch to help tab | `BaseMainWebFragment.java:31` |
| `/signOut` | logout | `BaseMainWebFragment.java:34` |
| `/purchase` | open purchase flow | `BaseMainWebFragment.java:37` |
| `/goToAgreement` | in-app agreement page | `BaseMainWebFragment.java:40` |
| `/goToPrivacy` | in-app privacy page | `BaseMainWebFragment.java:46` |
| `/innerWeb` | open `Path` **relative to the account base** — arbitrary same-origin navigation | `BaseMainWebFragment.java:52` |
| `/reLogin` | force re-login dialog | `BaseMainWebFragment.java:60` |
| `/goToBatterySetting` | OEM battery settings | `PreventDisconnectFragment.java:40` |
| `/goToAutoStartSetting` | OEM autostart settings | `PreventDisconnectFragment.java:64` |
| `/goToMiuiSetting` | MIUI-specific settings | `PreventDisconnectFragment.java:67` |
| `/shareInvitationPage` | invite/share page | `InvitationFragment.java:50` |
| `/changeAccountSuccess`, `/changePasswordSuccess` | post-edit navigation back into profile | `ModifyAccountWebViewFragment.java:65-106` |
| `/pay` | payment result callback | `PurchaseFragment.java:244` |
| `js://localWebPay` + `localWebFiles/sdkConfig.json` | local payment SDK harness | `p000/tt0.java:55-58`, `p000/wt0.java:57` |

`/innerWeb` matters for a dashboard: it resolves any relative path against `uu0.f11478k`, so the
web layer can pull the app into any same-origin page. The only other JS bridge in the APK is
Alipay's (`p000/fv0.java:158`, `AlipayChinaMobileBridge`).

## 6. Auth, session and signing

1. **Credential handoff.** `LoginActivity.m4188A(account, password)` →
   `appSetAccountNamePassword` → `appSetLoginInfo(account, password)` (native), then
   `appApiLoginSync(id, "login", null)` (`LoginActivity.java:70-77`). The password never appears
   in a Java request map; native builds `ClientApiLogin/Login`.
2. **Response.** `LoginSync.Login` fields consumed: `AuthResult` (`"OK"` = success,
   `C0023am.java:602-604`), `AuthMessage`, `LoginToken` → `_client_login` on later calls,
   `ExpireTime` (unix seconds, minus 10 s skew guard), `Expired`, `UseCount`, `UserName`,
   `ConnectPassword`, `Email`, `PhoneNumber`, `MembershipStatus`, `ApiIntervalMessage` (floored
   to 300 s), `FreeUsageTimeLeft` (default 1200), `TicketUnreadCount`. Parse:
   `C0023am.java:211-243`.
3. **Tunnel credential.** The tunnel does not reuse `LoginToken`. `FVService` builds
   `userName = <Login.UserName>[#<ClientSysEnvId>]` and `password = <Login.ConnectPassword>`
   (`FVService.java:555-559`), then `FVClient.prepare()` encrypts the auth blob with the embedded
   RSA-1024 public key (`RunnableC1787y7.java:423-424`; modulus byte-identical to
   `emulator/internal/fv/crypto.go:37`). `protocolVersion` is fixed at `2` in Java
   (`FVClient.java:42`) and never written again; native references the name at `0xbb84b`.
4. **Session cookie.** The web layer keeps a `userSha` cookie and re-installs it after
   `removeAllCookies` on logout (`FVNetClient.java:140-155`) — the only cookie name hardcoded in
   Java. Native keeps its own libcurl cookie jar and verifies it (`cookie verification failed`,
   `check cookie: sid_len=%u > %u` strings in `libfvcore.so`).
5. **TLS.** No CA bundle, no pin string and no Android `X509TrustManager` bridge exists in
   `libfvcore.so` (searched `.pem`, `.crt`, `cacert`, `TrustManager`, `KeyStore`,
   `CURLOPT_PINNEDPUBLICKEY`; only libcurl/mbedtls internals found). `Curl_pin_peer_pubkey` is
   *present in libcurl* but no caller was located. Combined with `research/82-live-20261023`
   row 6 (the shipping client accepts the vendor's self-signed `CN=localhost` leaf), the
   effective behavior is verification-off. **Exact curl option site: `[UNVERIFIED]`** — the
   stripped binary did not yield a `curl_easy_setopt` xref for `CURLOPT_SSL_VERIFYPEER` within
   this slice.
6. **Logout.** `appUserLogout` → clears native state; `removePassword()` wipes `Password` and
   `ConnectPassword` prefs; `clearCookiesAndCache()` (`FVNetClient.java:348-353`).

## 7. Device identity / request fingerprint

Built once in `FVApp.m4183d()` (`FVApp.java:139-231`) into `uu0.f11462G`, then frozen into
`uu0.f11463H` and sent on every `/client.php` call and inside the `LoginSync` cache key.

| Param | Source | Anchor |
|---|---|---|
| `_site` | constant `"ajs"` | `FVApp.java:152` |
| `_m` | constant `"android"` | `FVApp.java:154` |
| `_v` | `"4.12.2.0"` | `FVApp.java:143,153` |
| `_os_ver` | `Build.VERSION.RELEASE` | `FVApp.java:155` |
| `_os_dev` | `MANUFACTURER MODEL` normalised (`ks0.m7611j`) | `FVApp.java:156` |
| `_os_hw` | `Build.HARDWARE` | `FVApp.java:157` |
| `_os_did` | `Settings.Secure android_id` | `FVApp.java:145-149,158` |
| `_os_dim` | `{"asid":"<android_id>"}` (JSON inside a form field) | `FVApp.java:175-180` |
| `_os_lang` | `Locale.getLanguage() + "-" + getCountry()` | `FVApp.java:159` |
| `_tz` | `rawOffset/60000` rendered as `((m%60)+(m/60)*100)*sign`, e.g. `+08:00` → `800` | `FVApp.java:160-172` |
| `_vrf` | `UtmCampaign` pref, else APK ZIP-comment `channel` (absent here → `""`) | `FVApp.java:189`, `376-392` |
| `_inst_mkt` | installer package; `com.android.vending`→`google`, `com.amazon.venezia`→`amazon` | `FVApp.java:191-198` |
| `_lang` | constant `"zh_cn"` | `FVApp.java:198` |

Separately, the tunnel carries `osDeviceId = android_id`, `osDeviceIdMap = {"asid": …}`,
`osDevice`, `osVersion`, `osPlatform = "android"`, `clientVersion`, `clientSite = "ajs"`,
`clientExtra = ""` (`RunnableC1787y7.java:222-232`). `appClientUniqueId()` (native, `0x14f3b0`)
is a second, independent id used as `_uid` in web URLs.

**Dashboard implication:** the fingerprint is 12 fields, 6 of which are build-derived and
device-independent. A dashboard that wants to look like the Android client only needs
`_site/_m/_v/_os_ver/_os_dev/_os_hw/_os_lang/_tz/_lang/_inst_mkt` plus a per-install
`_os_did`/`_os_dim`.

## 8. Feature flags

Three independent flag mechanisms exist:

1. **APK ZIP-comment channel block** (`FVApp.java:182-188`, reader `cm0.m825r` →
   `uu0.m9073i()`). Keys: `channel` (→ `_vrf`), `appUpdateDisabled`,
   `reportAppsListDisabled`, `promptSpecifyAppsPermission`, `noOtherWaysToPurchase`.
   Consumers: `UpdateActivity.java:77`, `AboutUsFragment.java:36`, `MainActivity.java:347,411`,
   `C1689vk.java:97`. In this sample the ZIP comment is **empty**, so all four default `false`.
2. **`LoginSync.MarkFeature`** — a map of named server marks (`Level`, `DisplayOrder`,
   `HoverTip`, `Connectable`, `ConnectTip`) parsed into `C1417ol`
   (`C0023am.java:262-285`). Marks are matched against each node's `marks[]`
   (`C0023am.java:525-542`).
3. **`LoginSync.MobileFeature` / `ServerLoad` / `GooglePlay`** — `IsMessageCenterDisplay`,
   `ShareIconUrl`, `UserLoadStatus.full[]` (server-load full list), and Play product ids split
   into `onetime`→`inapp` / `auto`→`subs` (`C0023am.java:294-421`).

## 9. `LoginSync` response schema (the dashboard's primary data source)

Parsed by `C0023am.m96b()` (`p000/C0023am.java:184-425`). Field names are verbatim; all reads are
defensive (`opt*` with defaults).

```
AppUpdate      { UpdateRequired, UpdateOptional, UpdateChecksum, LatestVersion,
                 ClientUpdateTips, UpdateInMarket }          // C0023am.java:189-198
Login          { UrlWeb, LoginToken, AuthResult, AuthMessage, ExpireTime,
                 Expired, GroupTitle, UseCount, UserName, ConnectPassword, Email,
                 PhoneNumber, MembershipStatus, ApiIntervalMessage,
                 FreeUsageTimeLeft, FreeUsageAllowedLeft, TicketUnreadCount,
                 ShowAntiFraud[] }                            // C0023am.java:211-243
MobileUrl      { UrlMobile }                                  // C0023am.java:205-209
MarkFeature    { <MarkName>: { DisplayOrder, Level, HoverTip, Connectable,
                 ConnectTip } }                               // C0023am.java:262-285
Invitation     { ShareInviterUrl, ShareInviterText, InvitationBanner }
MobileFeature  { IsMessageCenterDisplay, ShareIconUrl }       // C0023am.java:296-301
ServerLoad     { UserLoadStatus: { full: [ <vsid>, … ] } }     // C0023am.java:302-322
PresetRoute    { List: [ { Code, Title, Checksum } ] }         // C0023am.java:323-345
Servers        { <serverId>: <node> }                          // node schema in §10
GooglePlay     { Products: [ "onetime", "auto", … ] }          // C0023am.java:373-403
ServerSearchHotKeywords { HotMark: [ str, … ] }                // C0023am.java:404-421
```

Update-decision helper: `m102h()` returns true when `LatestVersion != "4.12.2.0"` and either
`UpdateRequired` or `UpdateOptional` is non-empty (`C0023am.java:594-599`).

## 10. Server node schema and connect modes

### 10.1 Node

Parsed by `C0023am.m99e(serverId)` (`C0023am.java:472-566`). Nodes are built lazily and cached in
`f129j`.

```
Servers.<serverId> = {
  vsid : "<server virtual id>",          // key into ServerLoad.UserLoadStatus.full[]
  tit  : "<title>",                      // e.g. "镇江 #1"
  cmt  : "<comment>",
  ctry : "<country>",                    // read then discarded
  svr  : "<connect host>",               // hostname/IP handed to the tunnel
  sts  : "ok" | <error>,                 // only "ok" is connectable
  ord  : <int order>,
  par  : { fv: { u: <udp port>, p: <proxy port>, ps: "<pingSecret>" } },
  loc  : [ { c: "<code>", n: "<name>", loc: [ … ] } ],   // recursive hierarchy
  marks: [ { MarkName, MarkLabel, TipDatetime } ]        // first 3 only
}
```

- A node is usable only when `ps` is non-empty (`C0023am.java:495,497`), `sts == "ok"`, and
  `f10079m == null` — i.e. no *blocking* mark, a mark whose `MarkFeature.Connectable` is
  `false` (`C0023am.java:534-536`).
- Ping target strings are synthesised from the node:
  `vpn://<svr>:<par.fv.u>/?pingSecret=<ps>&id=<serverId>` and
  `proxy://<svr>:<par.fv.p>/?pingSecret=<ps>&id=<serverId>`
  (`C1466pl.m8362a/m8363b`, `p000/C1466pl.java:62-69`). The same strings are parsed natively by
  `FVPingManager::parseTargetItem` (`vpn://` @ `0xb7a97`, `proxy://` @ `0xb7a8e`,
  `pingSecret` @ `0xc7056`).
- The `loc` tree is flattened into `FVModelCategory` nodes with
  `mCode/mName/mOrder/mLevel/mChildCategories/mServerIds` (`C0023am.m94a`,
  `C0023am.java:134-172`); parents collect child server ids, and the tree is sorted by `ord`
  (`m95j`).

### 10.2 Connect modes

`hn0.f7865f` (`SelectedImplTypeIndex`) is the user's mode. Values map to `strings.xml` labels:
`0 = 自动模式 (auto)`, `1 = 极速模式 (UDP/VPN)`, `2 = 兼容模式 (TCP/proxy-tun)`.

Resolution to the implementation actually written into `PrefService.ImplType`
(`C0060bl.java:381-390`):

```
udpUsable = node.par.fv.u > 0 && pingReport(node).udpResult >= 0
auto       : ImplType = udpUsable ? 0 : 1
"vpn"      : ImplType = 0
"proxy-tun": ImplType = 1
```

`FVClient` constants: `ImplType_Vpn = 0`, `ImplType_ProxyTun = 1`; `RouteMode_Inclusion = 1`,
`RouteMode_Exclusion = 2`; states `0 Stopped / 100 Prepared / 200 Connected`
(`FVClient.java:23-29`). `ImplType == 0` uses `PrefService.ServerPortUdp`, `ImplType == 1` uses
`PrefService.ServerPortProxy` (`RunnableC1787y7.java:213-218`).

The mode the client reports back to the vendor (`ClientSettings[Protocol]`) is `vpn`,
`proxy-tun` or `auto` — i.e. the vendor sees the *mode*, not the resolved implementation
(`FVNetClient.java:369-378`).

### 10.3 Connect contract (`PrefService` shared prefs)

Written by `C0060bl.m611j` (`C0060bl.java:85-139`), read by `FVService.onStartCommand`
(`FVService.java:536-570`). This is the complete parameter set for a connect:

`NotificationText`, `ImplType`, `ServerId`, `ServerAddress`, `ServerPortUdp`, `ServerPortProxy`,
`AutoReconnectType`, `NetworkLock`, `RouteMode`, `SpecifiedApps`, `SpecifiedAppsArray`,
`CustomDnsServers`, `ClientSysEnvId`, `RouteNetworkCode`, `RouteNetworkCheckSum`,
`IsReconnection`, `ReconnectEndTime`.

Service intent actions: `com.fvcorp.fvservice.start` / `com.fvcorp.fvservice.stop`
(`FVService.java:566-568`, `C0060bl.java:131-133`).

## 11. Native `libfvcore.so`

### 11.1 Identity

| Item | Value |
|---|---|
| Path | `apk/lib/arm64-v8a/libfvcore.so`, 3 429 688 B |
| Format | ELF64 aarch64, `os=android`, PIC, NX, full RELRO, stripped, `lang=jni` |
| Other ABIs | `armeabi-v7a` 2 312 012 B; `x86` present; `x86_64` 3 418 528 B (core only) |
| Exports | 4674 total, of which **55** are `Java_com_fvcorp_android_fvcore_*` |
| Static deps | libcurl + mbedTLS + libevent2, all statically linked inside the `.so` |

### 11.2 JNI surface (55 exports)

| Java class | Count | Methods |
|---|---|---|
| `FVClient` | 18 | `attachDev`, `clean`, `getClientVirtualIp`, `getDevMtu`, `getDnsServersNative`, `getFailureMessage`, `getFailureType`, `getNetRxBytes`, `getNetTxBytes`, `getServerVirtualIp`, `getState`, `isWorkStateChanged`, `onClientNetReady`, `prepare`, `resetState`, `switchStateStopped`, `test`, `work` |
| `FVNetClient` | 20 | `appApiLoginSync`, `appCachedNetworkDataString`, `appCachedResponseExpired`, `appCachedResponseMtime`, `appCachedResponseString`, `appCancel`, `appCheckPageKeyword`, `appClientUniqueId`, `appDownloadNetworkData`, `appHttpRequestParams`, `appInit`, `appRefreshCachedLoginSync`, `appRemoveCachedResponse`, `appSetLoginInfo`, `appUserIsAnonymous`, `appUserLogout`, `httpRequestCancel`, `httpRequestCancelAll`, `httpRequestNewId`, `httpRequestStart` |
| `FVPingManager` | 7 | `fetchReportsNative`, `getReportNative`, `init`, `pingMulti`, `pingSingle`, `stopAsync`, `stopSync` |
| `FVCore` | 6 | `BuildVersion`, `LoggerFlush`, `LoggerResetFilenameNative`, `LoggerSetPrintLevelNative`, `LoggerWrite`, `Test` |
| `FVRoute` | 4 | `cleanup`, `generateRoutesNative`, `prepareRoutes`, `processRoute` |

Three `FVCore` exports (`LoggerFlush` @ `0x14d1e4`, `LoggerResetFilenameNative` @ `0x14d084`,
`Test` @ `0x14d304`) have **no Java counterpart** in this dex — the decompiled `FVCore.java`
declares only `BuildVersion`, `LoggerSetPrintLevelNative`, `LoggerWrite`. Dead JNI surface, i.e.
version skew between the Java layer and the core.

### 11.3 Internal architecture (from retained C++ symbols)

`FVNetClient` / `FVNetClient2` (HTTP client + cache), `FVHttpClient` (+ nested `Internal`,
`Mocker`), `FVClient` → `FVClientVpn` | `FVClientProxy` | `FVClientProxySocks` (implementations),
`FVProxy`, `FVProxyRequestHeader` (local HTTP/SOCKS front end), `EvConn` / `EvConnLocal` /
`EvConnRemote` (`LocalHandshakeResult`, `guessPrefetchBufSocks5Req`,
`guessPrefetchBufHttpReq` — protocol sniffing on the local side), `FVDnsProxy`,
`FVNetIpv6Nat64` + `FVNatItemMap` (NAT64 for IPv6-only networks), `FVRoute` (route-table
generation), `FVPingManager` (`ItemDataProxy` / `ItemDataVpn`, `evcb_pingudp_read`), `FVSys`,
`FVClientCommon` (failure-type constants).

Consequences for a clone:
- `FVClientProxySocks` is a **third** implementation the Android UI never selects (`ImplType` is
  only 0/1) — SOCKS5 is reachable only through the ping path and the local front end.
- `FVNetIpv6Nat64` means the tunnel must tolerate IPv6-only carriers; a dashboard probing
  reachability over IPv4-only will mislabel those nodes.

### 11.4 Selected string anchors (file offsets)

| Offset | String | Meaning |
|---|---|---|
| `0xc3ebf` | `ClientApiLogin/Login` | login command, native-only |
| `0xbaa9d` | `ClientApiClientSetting/LoadRoute` | route-data download |
| `0xc901b` | `ClientApiReport/WebsiteCheckReport` | censorship-probe report |
| `0xd0332` | `/client.php` | the single API path |
| `0x40094` | `LoginSync` | cache key for the login response |
| `0xc7041` | `WebsiteCheck` | cache key for the probe result |
| `0xc6330` | `CheckUrlKeyword` | `LoginSync` field consumed only by native |
| `0xc7056` | `pingSecret` | ping URL parameter |
| `0xb7a97` / `0xb7a8e` | `vpn://` / `proxy://` | ping target schemes |
| `0xcaed2` | `/CacheNetwork/network-` | route-data cache file prefix |
| `0xc20cc` | `_client_login` | session token parameter |
| `0xb4cb1` | `AuthResult` | login verdict field |
| `0xc0594` | `LoginToken` | session token field |
| `0xbb938` | `PresetRoute` | route preset field |
| `0x3e703` | `Servers` | server tree field |
| `0xbb84b` | `protocolVersion` | tunnel protocol version (fixed at 2) |
| `0x3effb` | `FVCORE_BUILD_VERSION` | macro name only — **no version literal in the binary** |
| `0x42704` | `FailureType_AuthForbidden` | failure enum symbol |

`libfvcore.so` contains **no version string** of its own; `FVCore.BuildVersion()` is a
compile-time macro whose value is not recoverable statically. `[UNVERIFIED]` — needs a device run.

### 11.5 Ping report format

`FVPingManager.stringToReport` (`FVPingManager.java:71-86`) splits on `;` and needs ≥ 8 fields:

```
serverId ; result ; state ; udpResult ; ? ; ? ; ? ; text
```

`result` ∈ `{-1001 None, -1002 Timeout, -1003 Error}`; `state` ∈
`{-2001 None, -2002 Queued, -2003 Working, -2004 Over}`. `udpResult` is the field the connect
decision reads (§10.2). Ping expiry is `30 s` normally, `86400 s` for the daily sweep
(`FVPingManager.java:24-25`, `C0011aa.java:182`).

## 12. Update path

1. `LoginSync.AppUpdate` carries `LatestVersion`, `UpdateRequired`, `UpdateOptional`,
   `UpdateChecksum`, `ClientUpdateTips`, `UpdateInMarket` (`C0023am.java:189-198`).
2. `UpdateActivity.m4212a(1, loginSync)` runs at the end of every successful login
   (`LoginActivity.java:127`).
3. If `UpdateInMarket` is true or the installer is `com.android.vending`, the app opens the Play
   listing; otherwise it downloads an APK and hands it to the system installer
   (`UpdateActivity.java:320`, `344-365`), which is why `REQUEST_INSTALL_PACKAGES` is declared.
   The download uses the generic request builder with `localFilePath(...).reportProgress(true)`
   (`UpdateActivity.java:365`).
4. **Integrity check is MD5, not the transmitted algorithm-agnostic name suggests.**
   `UpdateChecksum` is passed as the intent extra `…UpdateActivity.checksum`
   (`UpdateActivity.java:308,345`) and compared by `f01.m6684r` — `MessageDigest.getInstance("MD5")`
   over the file. A pre-existing file that matches is reused; a mismatch is deleted and
   re-downloaded (`UpdateActivity.java:355-361`), and a post-download mismatch sets
   `failureMessage = "Bad checksum"` (`p000/an0.java:69`).
5. `appUpdateDisabled` in the APK channel block suppresses the whole path
   (`UpdateActivity.java:77`).
6. `ClientApiUpgrade/UpgradeRecord` is the *reporting* half: the client tells the vendor it
   upgraded from `PriorVersion` at `UpgradeDatetime` (`MainActivity.java:630-655`).

## 13. What the client uploads — do not replicate

`ClientApiReport/Report` (`C1689vk.m9165b`, `p000/C1689vk.java:62-291`) is the privacy-sensitive
call. Per action (`login|resume|connect|disconnect|switchNetwork`) it may attach:

- `networkData` (`C1689vk.java:226`): current SSID, BSSID (colons stripped), WLAN list, intranet
  IPv4, netmask, gateway, DHCP server, DNS servers, SIM/network operator + country ISO.
- `deviceData` (`C1689vk.java:242`): `isRooted` (`C1689vk.java:233`; checks `/system/bin/su`,
  `/system/xbin/su`, `/sbin/su`, `/su/bin/su`, `/system/app/Superuser.apk`, `/data/local/bin/su`,
  … — the `/…/su` string block in the APK), `MANUFACTURER`, `MODEL`, locale, resolution, dpi,
  orientation, OS version.
- `appData` (`C1689vk.java:270`): **every installed app** with `pkgName`, `appName`, `verCode`,
  `verName`, `appSig` (signing cert), `firstInsTime`, `lastUpdTime` — gated by
  `reportAppsListDisabled`, which is absent here, so enabled (`C1689vk.java:97`).
- `ClientApiReport/WebsiteCheckReport` additionally reports `remoteIp`/`remotePort`/`httpCode`
  of a keyword probe (`libfvcore.so` `0xbf685` log format string, `0xc901b` command).

A dashboard must render server/membership state without any of this. None of it backs a
user-visible feature, and it is the single largest privacy delta between this client and the
panel.

## 14. Feature → dashboard table

| Dashboard capability | Source of truth | Android anchor | Effort | Notes |
|---|---|---|---|---|
| Server list / tree | `LoginSync.Servers` | `C0023am.java:184-566` | S | Already in `research/40-protocol`; Android confirms the same field names and adds `marks[]` |
| Per-node host + ports | `Servers.<id>.svr` + `par.fv.{u,p}` | `C0023am.java:491-513` | S | `u` = UDP/VPN port, `p` = proxy port; **string-typed on the wire**, parse leniently |
| Per-node ping secret | `Servers.<id>.par.fv.ps` | `C0023am.java:495` | S | Required to build `vpn://`/`proxy://` targets |
| Node connectability | `sts == "ok"`, `ps != ""`, no blocking mark | `C0023am.java:497,534` | S | Three conditions, not one |
| Server marks / badges | `marks[]` × `MarkFeature` | `C0023am.java:262-285, 525-542` | M | `Connectable=false` marks are *blocking*; `DisplayOrder` orders them |
| Node load / "full" | `ServerLoad.UserLoadStatus.full[]` (by `vsid`) | `C0023am.java:302-322` | S | Simple set membership |
| Latency / UDP reachability | native `FVPingManager` `;`-format report | `FVPingManager.java:71-86` | M | `udpResult` is also the auto-mode decision input |
| Connect mode: auto / UDP / TCP | `SelectedImplTypeIndex` + `udpUsable` | `C0060bl.java:381-390` | S | Labels 自动 / 极速 / 兼容 |
| Live connection state | `FVConnectionState` (state, implType, failureType, rx/tx) | `FVConnectionState.java:19-24`, `FVService.java:145-156` | S | States 0/100/200; failure types §14.1 |
| Traffic counters | `getNetRxBytes` / `getNetTxBytes` | `FVClient.java:89-91` | S | Byte counters from native |
| Membership / quota | `Login.MembershipStatus`, `ExpireTime`, `FreeUsageTimeLeft`, `UseCount` | `C0023am.java:216-243` | S | `ExpireTime` in seconds; client subtracts 10 s |
| Account pages (profile, orders, usage, KYC) | web routes W8–W18 | `uu0.java:390-401` | S | Reuse the panel's existing web session |
| Purchase / plan matrix | `GooglePlay.Products`, `/purchase` | `C0023am.java:373-403`, `uu0.java:383` | M | Split `onetime`→`inapp`, `auto`→`subs` |
| Favourites | `ClientApiUser/VpnServerFavorite*` | `nd0.java:429`, `ServerGroupsFragment.java:604` | S | Local list exists too (`FavoriteServerIds`) |
| Announcements / message center | `ClientApiMessageCenter/*` | `MainActivity.java:222-231` | S | `GetAnnounce` returns `Content` |
| Update available | `AppUpdate` + `m102h()` | `C0023am.java:189-198, 594-599` | S | Compare against our own version, not `4.12.2.0` |
| Device/install identity | 12-field fingerprint | `FVApp.java:139-231` | S | Section 7 |
| Auto-reconnect behaviour | `SelectedAutoReconnectType` → `once\|continuously\|sameCity` | `FVNetClient.java:157-167` | S | Plus `ReconnectEndTime` |
| Split tunnelling | `SpecifiedApps` + `SpecifiedAppsMap` | `hn0.java:143-152` | M | Android-only concept; not needed for a proxy dashboard |
| Custom DNS | `CustomDns` (comma list) → `ClientSettings[CustomDns]` | `hn0.java:129`, `FVNetClient.java:408` | S | `{dns1,dns2}` JSON |
| Route presets | `PresetRoute.List` + `LoadRoute` cache | `C0023am.java:323-345`, native `0xbaa9d` | L | Needs the native route generator to be useful |
| Censorship/network health probe | `CheckUrlKeyword` + `WebsiteCheckReport` | native `0xbf685`, `0xc901b` | L | **Do not build**; listed to explain vendor behavior |

### 14.1 Failure types (diagnostics vocabulary)

From `FVClient.java:12-22` and the `FVClientCommon` symbols in `libfvcore.so`:
`"" (none)`, `dev`, `internal`, `net`, `radius`, `auth.password`, `auth.group`, `auth.forbidden`,
`auth.membership`, `auth.limit`, `auth.off`, `session.timeout`, `timeout`. Android adds
`auth.kyc` handling in `FVConnectionState.isAuthKyc()`. A dashboard's error column should use
these exact tokens so one vocabulary spans platforms.

## 15. Divergences vs the 4.2.3.0 / Linux profile

| Fact | 4.2.3.0 / Linux (`research/82`, `40-protocol`) | Android 4.12.2.0 (this slice) | Consequence |
|---|---|---|---|
| Tunnel RSA key | 1024-bit modulus in `emulator/internal/fv/crypto.go:37` | **byte-identical** modulus, e=65537 (`RunnableC1787y7.java:423-424`) | No key rotation between builds; emulator assumption holds |
| Control host list | four hosts incl. `login.mmegg.com:4443` | same four, fixed order (`ju0.java:37`) | Host list is data, still overridable via `CustomLoginUrl` |
| Session token | `LoginToken` | same, plus `_client_login` on every call | unchanged |
| Protocol version | 2 | `protocolVersion = 2` fixed (`FVClient.java:42`) | unchanged |
| Mode vocabulary | `protocol socks5` (CLI) | `auto` / `vpn` / `proxy-tun` (`FVNetClient.java:369-378`) | Dashboard should speak the *client* vocabulary and map to transport internally |
| Server node fields | `par.fv.u/p`, `svr`, `vsid`, `sts`, `ord`, `loc[]` | same, **plus** `marks[]` and `ServerLoad.UserLoadStatus.full[]` | New fields; old parsers must ignore them, not fail |
| Settings reporting | not present | `ClientApiClientSetting/ClientSetting` on connect | New vendor-visible surface |
| Ping report | not in the 4.2.3.0 corpus | 8-field `;` string incl. `udpResult` | Needed to reproduce auto-mode behavior |
| TLS verification | self-signed `CN=localhost` accepted | no CA bundle / no pin found; same behavior implied | Both clients effectively disable verification — keep our pin |

## 16. Unverified / what remains

- `[UNVERIFIED]` **Exact TLS verification setting.** No `curl_easy_setopt` xref for
  `CURLOPT_SSL_VERIFYPEER` / `CURLOPT_SSL_VERIFYHOST` / `CURLOPT_PINNEDPUBLICKEY` was recovered
  from the stripped `.so`. Behavior inferred from `research/82` row 6, not proven here.
- `[UNVERIFIED]` **`FVCore.BuildVersion()` value.** No version literal in `libfvcore.so`;
  `0x3effb` is only the macro name.
- `[UNVERIFIED]` **APK signature scheme version (v2 vs v3).** The signing block is present at
  `0x124b305`; no `apksigner` in the station toolset to print the scheme.
- `[UNVERIFIED]` **`ClientApiClientSetting/LoadRoute` and `ClientApiVpnServer/AutoMatch` response
  shapes.** Request shapes are pinned (E4, E5); responses are known only by the fields the
  callbacks read (`Result`, `Error`, `PopupType`, `Data.VpnServerId`).
- `[UNVERIFIED]` **`appClientUniqueId()` derivation.** Native-only (`0x14f3b0`); format and
  persistence not reversed.
- `[UNVERIFIED]` **`CheckUrlKeyword` probe URL list.** The field name exists only in native
  (`0xc6330`); the URL list and interval (`LastCheckPageTime` @ `0xb6c50`) come from a `LoginSync`
  subset the Java parser does not expose.
- **Not attempted:** `armeabi-v7a` / `x86` / `x86_64` slices of `libfvcore.so`; any dynamic run.
- jadx reported 17 decompilation errors out of 1834 classes; none of the classes cited above was
  among them (each was read in full).

## 17. Reproduce

```bash
# 1. verify sample
sha256sum "C:/Users/dwgx1/Downloads/AJiaSu_4.12.2.0_ajs-official-website.apk"
#   -> 42ce2b5152c767c3f7412d65793aa15c294e539fb1f499428103205a352df3cb

# 2. manifest + resources
JAVA_HOME=D:/Software/Developer/jdk/21 java -jar \
  "D:/Software/Tool/WorkStation/debugger/Mobile-Android/Apktool/apktool_3.0.3.jar" \
  d -f -s -o apktool-out "C:/Users/dwgx1/Downloads/AJiaSu_4.12.2.0_ajs-official-website.apk"

# 3. java sources
JAVA_HOME=D:/Software/Developer/jdk/21 \
  "D:/Software/Tool/WorkStation/debugger/Mobile-Android/jadx/bin/jadx.bat" \
  -d jadx-out --no-res --show-bad-code --deobf apk/classes.dex

# 4. signer
unzip -o -q <apk> META-INF/CERT.RSA -d sig
D:/Software/Developer/jdk/21/bin/keytool.exe -printcert -file sig/META-INF/CERT.RSA

# 5. native exports
RB=D:/Software/Tool/WorkStation/debugger/Static-Reversing/radare2/radare2-6.2.2-w64/bin/rabin2.exe
"$RB" -I apk/lib/arm64-v8a/libfvcore.so
"$RB" -E apk/lib/arm64-v8a/libfvcore.so | grep Java_com_fvcorp

# 6. native strings with offsets
python -c "d=open('apk/lib/arm64-v8a/libfvcore.so','rb').read(); \
  [print(t, hex(d.find(t.encode()))) for t in \
  ['ClientApiLogin/Login','/client.php','LoginSync','pingSecret','vpn://','proxy://']]"

# 7. signing block
python -c "d=open('<apk>','rb').read(); print(hex(d.find(b'APK Sig Block 42')))"
```
