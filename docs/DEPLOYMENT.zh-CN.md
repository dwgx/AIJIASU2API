# 部署

状态：适用于 `v0.1.0-pre.3`（2026-10-29 发布）。上一已发布版本为 `v0.1.0-pre.2`
（2026-10-29 发布）。English：
[`DEPLOYMENT.md`](DEPLOYMENT.md)。

支持三种部署形态，彼此不可混用：加固的 Linux/systemd 安装、基于已发布镜像的 Docker Compose
栈，以及 Windows 可执行文件。按主机选择其一。三种形态的校验默认值并不相同——
以[校验工具](#校验工具)一节的表格为准，不要凭习惯。

线上演示主机**不是**本文流程的示例：它运行的是 `ajs-accountd-61625b0`，早于本版本，且位于
scratch 路径。不要用它推断当前版本，参见
[`OPERATIONS.md`](OPERATIONS.md#live-host-as-built)。

## 选择模式

| 模式 | 主机 | 目录 | 安装 | 更新 | 回滚 |
|---|---|---|---|---|---|
| Linux + systemd（HMC） | Debian/Ubuntu，systemd ≥ 247 | [`deploy/production/`](../deploy/production/README.md) | 二进制 + unit + nginx | 手工、先校验再替换 | 旧二进制 + 升级前状态 |
| Docker Compose | Docker Engine 29+，Compose v2.24+ | [`deploy/`](../deploy/README.md) | `update.sh` 拉取 GHCR 镜像 | `update.sh`（校验并锁定摘要） | `update.sh --rollback` |
| Windows EXE | Windows 10/11 或 Server，PowerShell 5.1+ | [`deploy/windows/`](../deploy/windows/) | `install.ps1` | `update.ps1`（失败自动恢复） | `update.ps1 -Rollback` |

三种形态的校验路径都使用 GitHub CLI（`gh`）。Windows 二进制没有 Authenticode 签名，发布产物
也都没有代码签名；真实性来自下文的证明（attestation），而不是平台签名。

## “已验证”的含义

发布身份是 tag 加上随附的 `MANIFEST.txt`，绝不是进程打印的版本字符串。共有四层，各自回答
不同的问题：

| 层 | 回答的问题 | 所在位置 |
|---|---|---|
| `SHA256SUMS` | 字节是否完整，是否就是本次发布的文件？ | 六文件发布 |
| `MANIFEST.txt` | 由哪个 tag、commit、工作区状态、工具链和编译参数产生？ | 六文件发布 |
| 构建来源证明 | 哪个工作流、哪个 commit 构建了这个文件或镜像？ | `gh attestation verify` |
| 不可变发布 + 发布证明 | 发布后 tag 与产物还能不能改？ | GitHub 仓库设置 |

### 校验和只证明完整性，不证明真实性

`SHA256SUMS` 与产物来自同一处、走同一条信任路径。能替换产物的攻击者可以在同一步替换校验和
文件，因此 `sha256sum -c` 通过只能说明“收到的文件与收到的那份清单一致”——它能发现损坏、
截断下载、镜像漂移和版本拿错，但不能证明文件由谁构建、是否来自本仓库。

真实性需要绑定到可信身份的签名或证明（这里是 `dwgx/AIJIASU2API` 的 GitHub Actions OIDC
身份），以及发布后不可改写的记录。缺少这一层时，你信任的是 GitHub 的 TLS、仓库账号和 tag
——对实验环境可能足够，但那是另一种主张，不能叫“已验证”。

### 不可变发布

仓库设置已开启，可自行核对：

```bash
gh api repos/dwgx/AIJIASU2API/immutable-releases --jq .enabled   # true
```

对在设置开启后发布的版本，GitHub 会锁定产物与 tag：发布存在期间 tag 不能移动或删除，产物不能
修改或删除，已删除发布的 tag 名不能被复用；标题和发布说明仍可编辑。发布不可变版本时还会生成
**发布证明**，绑定 tag、commit 与产物。

该设置没有锁定它之前发布的版本：API 报告 `v0.1.0-pre.2` 是可变的：

```bash
gh api repos/dwgx/AIJIASU2API/releases/tags/v0.1.0-pre.2 --jq .immutable   # false
```

因此 `v0.1.0-pre.2` 只是项目约定上的不可变。`v0.1.0-pre.3` 在设置开启之后发布，是第一个由
GitHub 强制执行的发布：

```bash
gh api repos/dwgx/AIJIASU2API/releases/tags/v0.1.0-pre.3 --jq .immutable   # true
```

### 构建产物证明

`.github/workflows/package.yml` 为六个发布文件生成证明，
`.github/workflows/release.yml` 为每个推送的镜像摘要生成证明，使用
`actions/attest-build-provenance`（权限 `id-token: write` 与 `attestations: write`；镜像工作流
另需 `packages: write` 以推送镜像）。这仅对**步骤落地之后**发布的 tag 生效。

在接收主机上校验文件或镜像：

```bash
gh attestation verify ./ajs-accountd-windows-amd64.exe --repo dwgx/AIJIASU2API
gh attestation verify oci://ghcr.io/dwgx/ajs-accountd@sha256:<digest> --repo dwgx/AIJIASU2API
gh release verify-asset <tag> ./ajs-accountd-linux-amd64 --repo dwgx/AIJIASU2API
```

`gh release verify-asset` 校验发布证明，只有不可变发布才有。

`v0.1.0-pre.2` 早于以上全部机制：在 `v0.1.0-pre.2` 这个 tag 上，发布工作流没有任何证明步骤
（`git show v0.1.0-pre.2:.github/workflows/release.yml | grep -c attest` → `0`）。因此对它做
证明校验会按设计失败关闭。`v0.1.0-pre.3` 是第一个带这些步骤发布的版本：六个产物都有构建来源
证明，两个镜像摘要都有证明，发布证明也通过校验。

### 发布流程：先草稿，再发布

在该流程落地之后发布的 tag，按不可变发布唯一接受的顺序进行：

1. `package.yml` 构建并为六个文件生成证明，把 GitHub 发布创建为**草稿**，附上全部六个产物，
   然后用 `gh release edit "$TAG" --draft=false` 发布。
2. `release.yml` 从不创建发布、也从不附加产物。它最多等待 10 分钟（`gh release view`）直到该
   发布出现，然后用 `gh release edit --notes-file` 更新说明；发布始终不出现则该任务失败。

原因就是不可变性：产物在发布时被锁定，所以先发布、后附产物的做法永远无法追加文件。草稿先行
可以先把文件附齐再发布。之后仍可更新说明，因为不可变发布保留标题与发布说明的编辑权。

对校验的影响：在该流程落地之后切出的 tag 是不可变发布，会自动带有发布证明，因此
`gh release verify-asset` 对它报告 `verified`。`v0.1.0-pre.3` 是第一个通过该流程发布的版本，
`immutable: true`。`v0.1.0-pre.2` 及更早版本由旧流程（镜像工作流创建发布）发布，没有发布证明。

### 摘要锁定

tag 是可移动指针，摘要（digest）是内容地址。容器侧应锁
`ghcr.io/dwgx/ajs-accountd@sha256:<64 位十六进制>`，而不是 `:v0.1.0-pre.2`。

- `deploy/update.sh` 会解析每个已拉取镜像的摘要，**针对该摘要**校验证明，把两个摘要写入
  `deploy/update-state.json`，并导出 `AJS_ACCOUNTD_DIGEST` / `AJS_DASHBOARD_DIGEST`，使启动的
  容器正是被校验的那些字节。
- 直接运行 `docker compose` 时，在 `deploy/.env` 里设置这两个变量（例如
  `AJS_ACCOUNTD_DIGEST=sha256:<64 位十六进制>`）；不设置则跟随 tag。
- 读取本地已有镜像的摘要：
  `docker image inspect --format '{{index .RepoDigests 0}}' ghcr.io/dwgx/ajs-accountd:v0.1.0-pre.2`。
- 二进制靠“保留原文件 + 配套 `MANIFEST.txt` 与 `SHA256SUMS`”来锁定，这条路径不涉及镜像仓库。

### 校验工具

| 工具 | 默认 | 严格 | 跳过校验 | 退出码 |
|---|---|---|---|---|
| [`deploy/verify-release.sh`](../deploy/verify-release.sh) | 六个产物都要求构建来源证明；有发布证明则校验，没有则报告 `absent` | `--require-attestation`（发布证明必需） | `--allow-unverified`（跳过证明；校验和与 MANIFEST 仍然强制） | 0 通过，1 检查失败，2 用法错误 |
| [`deploy/windows/verify-release.ps1`](../deploy/windows/verify-release.ps1) | `SHA256SUMS` + `MANIFEST.txt` 版本 + 二进制内嵌 `-version`，并默认要求两种证明 | `-RequireAttestation`（显式写法；本就是默认） | `-AllowUnverified`（证明失败降级为警告；哈希永不跳过） | 0 通过，1 失败，2 用法/前置条件 |

`deploy/verify-release.sh` 用 `gh release download` 把六个产物下载到 `${TMPDIR:-/tmp}` 下新建的
`mktemp -d` 目录（模式 700，脚本以 `dir:` 打印），拒绝 `latest`，依次检查：`SHA256SUMS` 且必须
覆盖每个产物；`MANIFEST.txt`（`version` 等于 tag，`commit` 为 40 位十六进制且等于 tag 指向的
commit，`worktree: clean`）；最后是证明。tag 的 commit 无法解析即失败：MANIFEST 的 commit 必须
能绑定到该 tag。`--dir` 仅在归当前用户所有、且组或其他用户不可写时才被接受。缺少 `gh` 一律
失败——下载依赖它；`gh` 不支持 `attestation verify` 时，除非给出 `--allow-unverified`，否则
失败。`--require-attestation` 与 `--allow-unverified` 同时给出属于用法错误（退出码 2）。

Windows 校验器只下载 Windows 可执行文件、`MANIFEST.txt` 和 `SHA256SUMS`，并额外运行下载的
二进制 `-version`、比对内嵌版本。无法在验证主机上执行的二进制会标记为 `[UNVERIFIED]`。

## Linux + systemd（HMC）

经过评审的套件在 [`deploy/production/`](../deploy/production/README.md)；下面的精简流程与
[`OPERATIONS.md`](OPERATIONS.md#install-from-the-kit) 是同一套步骤。

### 前置条件

- Debian/Ubuntu x86_64，systemd 247 或更新（`ProtectProc=invisible` / `ProcSubset=pid` 需要），
  具备 root 或 `sudo`。
- 发布产物 `ajs-accountd-linux-amd64`；主机上有 `gh` 以及 `sha256sum`（或 `shasum`）。
- 若使用公网 gate 边缘，需要 nginx 与 `libnginx-mod-stream`。
- 专用非 root 账号（`ajs`），`/etc/ajs` 放配置，`/var/lib/ajs` 放状态。

### 安装

```bash
# 1. 校验发布；记下脚本打印的 "dir:" 路径。
#    这是上一已发布版本，没有证明：
(cd deploy && ./verify-release.sh --tag v0.1.0-pre.2 --allow-unverified)
# -> dir: /tmp/ajs-release-verify.Ab12Cd   （新建的 mktemp -d，模式 700）
#    v0.1.0-pre.3 已发布，改用默认/严格形式：
#    (cd deploy && ./verify-release.sh --tag v0.1.0-pre.3 --require-attestation)

# 2. 身份、目录、二进制（以 root 执行）。
useradd --system --user-group --home /var/lib/ajs --shell /usr/sbin/nologin ajs
install -d -m 0750 -o root -g ajs /etc/ajs
install -d -m 0750 -o root -g root /etc/ajs/tls
install -d -m 0700 -o ajs -g ajs /var/lib/ajs
install -m 0755 <dir>/ajs-accountd-linux-amd64 /usr/local/bin/ajs-accountd

# 3. 环境文件与 unit。
install -m 0600 -o root -g root deploy/production/systemd/ajs-accountd.env.example \
  /etc/ajs/accountd.env
sudo -e /etc/ajs/accountd.env
install -m 0644 deploy/production/systemd/ajs-accountd.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now ajs-accountd

# 4. 健康检查。
curl -fsS http://127.0.0.1:2261/healthz
```

随后按套件 README 的第 4–7 步完成面板静态包、nginx、TLS 与操作员身份。unit 以 `User=ajs`
运行，带 `ProtectSystem=strict`、空 capability 集，只有 `/var/lib/ajs` 可写；`/etc/ajs` 对
守护进程只读。

### 校验

```bash
cd deploy
./verify-release.sh --tag v0.1.0-pre.3 --require-attestation   # 当前版本
./verify-release.sh --tag v0.1.0-pre.2 --allow-unverified      # 上一版本（无证明）
```

对 `v0.1.0-pre.3`，`--require-attestation` 是严格形式：额外要求只有不可变发布才有的发布证明。
已发布版本通过校验：退出码 0，`SHA256SUMS` 5/5，`MANIFEST.txt` commit `5479a3a`，构建来源
证明与发布证明均通过。

### 更新

systemd 主机没有 `update.sh`；`update.sh` 是 Compose 路径。替换是手工且先校验的：

```bash
cd deploy
./verify-release.sh --tag <new> --require-attestation   # 或 --allow-unverified

systemctl stop ajs-accountd
cp -a /var/lib/ajs/state.json /var/lib/ajs/state.json.pre-<new>
cp -a /usr/local/bin/ajs-accountd /usr/local/bin/ajs-accountd.previous

install -m 0755 <verified-dir>/ajs-accountd-linux-amd64 /usr/local/bin/ajs-accountd
# 若 unit 或 vhost 有变化，一并安装，然后：
nginx -t && systemctl daemon-reload
systemctl start ajs-accountd
curl -fsS http://127.0.0.1:2261/readyz
```

### 健康检查

```bash
curl -fsS http://127.0.0.1:2261/healthz    # 存活、版本与各状态账号数
curl -fsS http://127.0.0.1:2261/readyz     # 就绪：状态目录写探测 + 已配置集成
systemctl status ajs-accountd
journalctl -u ajs-accountd -n 200 --no-pager
```

### 回滚

停止守护进程，恢复旧二进制**和**升级前的状态文件，恢复旧的 nginx/stream 配置，`nginx -t`，
然后启动并检查 `/readyz`。状态快照是带版本的：本构建写 v2，v1 二进制会拒绝读取，因此恢复备份
是必需步骤，不是可选项。不要用删除新状态目录的方式“回滚”；把它留下以便排查。

### 配置保留

替换 `/usr/local/bin/ajs-accountd` 不会改动配置：`/etc/ajs/accountd.env`、
`/etc/ajs/admin-operators`、渲染出的哈希文件、TLS 材料和 nginx vhost 都保持原样。状态目录同样
不受替换影响——正因如此，升级前的副本必须单独、明确地做。每次备份都要一并保留
`AJS_MASTER_KEY`；没有它，`secrets.json` 里的密封值不可恢复。

## Docker Compose

栈由 [`deploy/compose.yml`](../deploy/compose.yml) 与
[`deploy/compose.release.yml`](../deploy/compose.release.yml) 组成，后者把本地构建换成 GHCR
镜像。

### 前置条件

- Docker Engine 29 或更新，带 Compose 插件（实际下限是 Compose v2.24+）。
- 回环端口 `2261`（守护进程）与 `18081`（面板）空闲。
- `deploy/.env` 含 `AJS_ADMIN_TOKEN`（32 字符以上）和 `AJS_MASTER_KEY`（64 位十六进制）；
  `AJS_GHCR_OWNER` 与 `AJS_IMAGE_TAG` 选择镜像命名空间和 tag。
- `gh` 用于镜像证明校验。
- 只有 compose 文件和 `.env` 需要本仓库，发布镜像本身不需要。
- `deploy/.env.example` 自带 `AJS_IMAGE_TAG=latest`；`update.sh` 拒绝这个浮动别名，除非给出
  `--allow-latest`，所以请传 `--tag` 或在 `.env` 里设置发布 tag。

### 安装

```bash
cd deploy
cp .env.example .env
# 编辑 .env：AJS_ADMIN_TOKEN、AJS_MASTER_KEY；把 AJS_IMAGE_TAG 设为发布 tag，
# 或给 update.sh 传 --tag（自带值 "latest" 在没有 --allow-latest 时会被拒绝）

# 首次从当前已发布镜像安装。v0.1.0-pre.3 带证明：
./update.sh --tag v0.1.0-pre.3

# 上一版本 v0.1.0-pre.2 早于证明机制：
# ./update.sh --tag v0.1.0-pre.2 --allow-unverified

# 或者直接运行 compose，不使用脚本的校验与状态文件
# （先把 AJS_IMAGE_TAG 设为发布 tag；自带的 "latest" 只是别名）：
docker compose -f compose.yml -f compose.release.yml up -d --no-build
```

`update.sh` 叠加两个 compose 文件，拉取、校验（除非显式跳过）、原子地写入 `update-state.json`
（模式 0600，已加入 gitignore）、启动容器，并等待两个健康端点。首次安装时记录的旧镜像 ID 是
`none`，所以 `--rollback` 没有可恢复的对象。

### 校验

- **镜像**——`./update.sh --tag <tag>` 默认在切换任何容器之前校验两个镜像。手工等价命令是
  `gh attestation verify oci://ghcr.io/dwgx/ajs-accountd@<digest> --repo dwgx/AIJIASU2API`，
  摘要来自 `docker image inspect --format '{{index .RepoDigests 0}}' <ref>`。
- **发布产物**——`./verify-release.sh --tag <tag>` 校验六个文件、`MANIFEST.txt` 与证明。它校验
  的是产物而不是镜像；`update.sh` 不校验发布产物。
- **摘要锁定**——`update.sh` 通过 `AJS_ACCOUNTD_DIGEST` / `AJS_DASHBOARD_DIGEST` 锁定它校验过
  的字节；直接运行 compose 时在 `.env` 里设置，或留空跟随 tag。

### 更新

```bash
cd deploy
./update.sh --tag <new>                    # 默认开启校验
./update.sh --tag <new> --verify           # 默认行为的显式写法
./update.sh --tag v0.1.0-pre.3              # 当前版本；默认校验证明
./update.sh --tag v0.1.0-pre.2 --allow-unverified   # 仅上一版本（无证明）
./update.sh --tag latest --allow-latest     # 仅当你确实要部署浮动别名
```

| 参数 | 作用 |
|---|---|
| `--tag <tag>` | 要部署的发布 tag；除非 `AJS_IMAGE_TAG` 已是发布 tag，否则必需 |
| `--verify` | 要求镜像证明（默认） |
| `--allow-unverified` | 跳过镜像证明；仍解析并记录摘要 |
| `--allow-latest` | 允许部署浮动 `latest` 别名；与 `--rollback` 同用时被拒绝 |
| `--rollback` | 恢复上次更新前记录的镜像 ID |

发布 tag 是必需的：既没有 `--tag` 也没有 `AJS_IMAGE_TAG` 时脚本以退出码 1 结束；浮动
`latest` 除非给出 `--allow-latest`，否则一律拒绝。由于 `.env.example` 自带
`AJS_IMAGE_TAG=latest`，直接运行 `./update.sh` 会失败，直到你传入 `--tag` 或在 `.env` 里设置
发布 tag。tag 字符限定为 `[A-Za-z0-9._-]`，其他字符一律失败（退出码 1）。

顺序是强制的：`gh` 能力检查（在任何拉取之前）→ `compose pull` → 解析摘要 → 校验两个镜像 →
写 `update-state.json` → 带摘要执行 `compose up -d --no-build` → 健康检查。`compose up` 之前
的任何失败都不会影响正在运行的部署；已拉取的镜像可能留在本地缓存。缺少 `gh` 或它不支持
`attestation verify` 时，运行会在任何拉取之前停止，并提示 `--allow-unverified` 作为跳过方式。

`--allow-unverified` 会连同证明范围一起记录在 `update-state.json`；成功摘要打印
`attestations: checksums verified; provenance skipped`，通过校验的运行则打印
`attestations: verified (repo:…|owner:…)`。两者都不跳过摘要解析或健康检查。`--verify` 与
`--allow-unverified` 同时给出属于用法错误（退出码 2）。

### 健康检查

```bash
curl -fsS http://127.0.0.1:2261/healthz    # 守护进程
curl -fsS http://127.0.0.1:18081/healthz   # 面板 nginx，返回 "ok"
docker compose -f compose.yml -f compose.release.yml ps
docker compose -f compose.yml -f compose.release.yml logs -f accountd
```

### 回滚

```bash
cd deploy
./update.sh --rollback
```

它恢复上次更新前记录的两个镜像 ID，并执行同样的健康检查。`update-state.json` 缺失或记录为
`none` 时拒绝执行；带 `--verify`/`--allow-unverified` 或 `--allow-latest` 也拒绝（退出码 2），
因为回滚不下载任何东西。回滚恰好一步：每次更新都会覆盖状态文件，要退得更远就在旧 tag 也早于
证明机制时执行 `./update.sh --tag <更旧> --allow-unverified`。

### 配置保留

`update.sh` 从不改写 `deploy/.env`；命名卷 `ajs-control-plane_accountd-data` 保存
`state.json` 与 `secrets.json`，在 `compose up -d` 和 `compose down` 之间保留。
`docker compose down -v` 会**销毁**该卷。大版本更新前备份该卷，并把 `AJS_MASTER_KEY` 与备份
一起保管。`update-state.json` 是运行时状态，原子写入、模式 0600 且已加入 gitignore；它记录
tag、证明范围，以及供 `--rollback` 使用的旧镜像 ID 与摘要。

## Windows EXE

脚本位于 [`deploy/windows/`](../deploy/windows/)：`install.ps1`、`update.ps1`、
`verify-release.ps1`、`run.ps1`，完整手册见
[`deploy/windows/README.md`](../deploy/windows/README.md)。它们只安装守护进程；面板是独立的
静态包或容器。

### 前置条件

- Windows 10/11 或 Server x64，Windows PowerShell 5.1 或 PowerShell 7。
- `gh` 在 `PATH` 中并已登录（`gh auth login`），用于下载与证明校验；没有它，校验器会在下载
  之前以退出码 2 结束。
- 注册自启动任务、写入 `C:\ProgramData\AIJIASU2API` 需要提升权限的终端。
- 守护进程的回环端口（默认 `127.0.0.1:2261`）。
- 没有服务包装：`ajs-accountd` 未实现 SCM dispatcher，`sc.exe create` 会以错误 1053 失败。
  安装器改为注册**计划任务**，开机以 `SYSTEM` 运行。
- 二进制未签名；SmartScreen 可能要求 **更多信息 → 仍要运行**。没有 Authenticode 签名，这些
  脚本也不配置 TLS 或反向代理。

### 安装

```powershell
cd deploy\windows
.\install.ps1 -Tag v0.1.0-pre.2 -AllowUnverified -WhatIf   # 上一版本；只打印计划，不写入
.\install.ps1 -Tag v0.1.0-pre.2 -AllowUnverified            # 校验、安装、启动、健康检查
# v0.1.0-pre.3 带证明；默认要求证明：
# .\install.ps1 -Tag v0.1.0-pre.3 -WhatIf
```

`-InstallDir`（默认 `C:\ProgramData\AIJIASU2API`）下的布局如下；旧二进制与更新记录在第一次
运行 `update.ps1` 之后出现：

| 路径 | 内容 |
|---|---|
| `ajs-accountd.exe` | 已校验的二进制，先暂存到目标旁边再移动就位 |
| `ajs-accountd.exe.previous` | 旧二进制，供 `update.ps1 -Rollback` 使用 |
| `run.ps1` | 自启动任务使用的启动器 |
| `ajs.env` | 只生成一次且**永不覆盖**；管理员 bearer 与静态主密钥，ACL 仅限 SYSTEM、Administrators 和安装账号 |
| `update-state.json` | 最近一次更新/回滚记录：tag、版本、旧版本、两个 SHA-256、健康 URL |
| `data\` | 状态目录，跨安装与更新保留 |
| `logs\` | 每次启动一对 `<timestamp>.out.log`/`.err.log`，保留最近 10 对 |

参数：`-Tag`、`-Repo`（默认 `dwgx/AIJIASU2API`）、`-InstallDir`、`-StateDir`、`-Listen`、
`-ServiceName`（默认 `AJSAccountd`）、`-NoService`、`-RequireAttestation`、
`-AllowUnverified`、`-Force`、`-TimeoutSec`、`-WhatIf`。`-NoService` 只安装文件、不启动；
`-WhatIf` 只打印计划，不下载不写入；`-Force` 就地替换二进制，但仍不覆盖 `ajs.env`。

### 校验

```powershell
.\verify-release.ps1 -Tag v0.1.0-pre.3                   # 当前版本
.\verify-release.ps1 -Tag v0.1.0-pre.2 -AllowUnverified  # 上一版本
```

校验和链（`SHA256SUMS`）、`MANIFEST.txt` 版本和二进制内嵌 `-version` 始终检查，且默认要求
构建来源证明与发布证明。`-AllowUnverified` 是唯一的跳过方式：把失败或不可用的证明检查降级为
警告，从不跳过哈希。`-RequireAttestation` 可显式给出（本就是默认），不能与 `-AllowUnverified`
同用（退出码 2）。`-PassThru` 返回已校验的路径、版本与 SHA-256，并保留下载目录供调用方使用。

### 更新

```powershell
.\update.ps1 -Tag v0.1.0-pre.3                   # 当前版本
.\update.ps1 -Tag v0.1.0-pre.2 -AllowUnverified  # 上一版本
```

`update.ps1` 先校验发布，停止计划任务和正在运行的守护进程，把当前二进制保留为
`ajs-accountd.exe.previous`，暂存并移动新二进制就位，启动守护进程并等待 `/healthz` 报告新版本。
若该检查失败，它会换回旧二进制并重启。配置文件与状态目录从不改动。最近一次更新记录在
`<InstallDir>\update-state.json`。

停止是硬停止：守护进程收不到优雅信号，最多丢失一个快照间隔的状态。`update.ps1` 会停止从安装
路径运行的所有 `ajs-accountd` 进程（不只是计划任务），并等待其退出后再替换二进制。

### 健康检查

```powershell
Invoke-RestMethod http://127.0.0.1:2261/healthz
Get-ScheduledTask -TaskName AJSAccountd
Get-Content .\logs\*.log -Tail 50
.\run.ps1 -Smoke        # 一次性运行：空闲回环端口、临时状态，/healthz 后停止
```

### 回滚

```powershell
.\update.ps1 -Rollback
```

它把 `ajs-accountd.exe.previous` 换回、启动并执行同样的健康检查。不下载、不重复校验：该二进制
在安装时已经校验过。

### 配置保留

`ajs.env` 只生成一次且永不覆盖（包括 `-Force`）；`data\` 跨安装与更新保留。旧二进制留在当前
二进制旁边，因此回滚不需要网络。请把 `ajs.env` 与 `data\` 复制到主机之外保存：配置里有管理员
bearer 与主密钥，状态里有这些密钥保护的密封值。

这些脚本实际观察到的是：针对 `v0.1.0-pre.2` 的校验、`install.ps1 -NoService`、不启用自启动的
`update.ps1`、`-Rollback` 以及 PowerShell 5.1/7 下的 `run.ps1 -Smoke`。已发布的
`v0.1.0-pre.3` Windows 二进制已校验，`-version` 输出 `ajs-accountd v0.1.0-pre.3 (5479a3a)`。
提升权限路径——任务注册、SYSTEM 启动、更新时健康检查与自动回滚——是代码路径而非本机实测：
本工作站上为 `[UNVERIFIED]`。依赖自启动前，请在目标主机上重跑。

## 面板：设置 → 更新

操作台从不自我更新。**设置 → 更新**提供三个只读步骤：

1. **检查**——`GET /v1/system/update`，只读的 GitHub 发布查询。仅当守护进程设置了
   `AJS_UPDATE_REPO` 时启用，否则卡片会说明未启用。
2. **复制校验命令**——`cd deploy && ./verify-release.sh --tag <latest> --require-attestation`。
3. **复制更新命令**——`cd deploy && ./update.sh --tag <latest> --verify`。

两条命令都是复制给人在主机上执行的；面板一条都不执行。卡片上写明：*“面板不会执行 root
更新：它只做版本检查、给出校验步骤并复制主机命令——请在主机上执行。”*

该检查调用 GitHub 的 `/releases/latest`，而它排除 pre-release。本仓库已发布的版本都是
pre-release，因此当 `AJS_UPDATE_REPO` 指向本仓库时，该检查返回 `502 UPSTREAM_UNAVAILABLE`，
两个复制行不会出现；等出现正式的（不含连字符的）发布后才会显示。无论是否显示，本文给出的命令
都可用。

不存在 root 更新端点：没有 `POST /v1/system/update`，守护进程以非特权身份运行，无法替换自己的
容器或二进制，静态面板也没有主机访问权。真正的“一键更新”需要特权更新器（root agent、Docker
socket 或主机 API），在本版本中是**非目标**：那会把面板失陷升级为主机 root。自动更新与回滚
尚未实现。

有一个需要知道的后果：复制出的校验命令是严格形式（`--require-attestation`），上一已发布版本
无法满足它。对 `v0.1.0-pre.2`，请走校验和路径：
`./verify-release.sh --tag v0.1.0-pre.2 --allow-unverified`，它仍然强制 `SHA256SUMS` 与
`MANIFEST.txt`。`v0.1.0-pre.3` 满足严格形式：
`./verify-release.sh --tag v0.1.0-pre.3 --require-attestation` 退出码为 0。

## 当前发布状态

| 项目 | `v0.1.0-pre.3`（2026-10-29 发布） | `v0.1.0-pre.2`（上一版本） |
|---|---|---|
| Tag / commit | tag 对象 `753122c`，指向 `5479a3a` | `v0.1.0-pre.2` 指向 `d80fee7`，2026-10-29 发布 |
| 产物 | 六个文件，GitHub prerelease；`:latest` 未移动 | 六个文件，GitHub prerelease；`:latest` 未移动 |
| 不可变发布 | **是**（`"immutable": true`） | **否**（`"immutable": false`）；仅项目约定 |
| 构建来源证明 | **有**——`package.yml` 覆盖六个文件，`release.yml` 覆盖两个镜像摘要；已校验 | **无**（该 tag 的工作流没有证明步骤） |
| 发布证明 | **有**——由不可变发布生成；`gh release verify-asset` 通过 | **无** |
| 发布流程 | `package.yml` 草稿 → 附加六个产物 → 发布；`release.yml` 生成镜像证明并更新说明（GHCR 一次瞬时重试） | 旧流程：`release.yml` 创建发布 |
| Linux 校验 | `--require-attestation` 通过（校验和与 `MANIFEST.txt` 始终强制） | `--allow-unverified`（校验和与 `MANIFEST.txt` 仍强制） |
| Windows 校验 | 默认强制证明；已发布二进制输出 `v0.1.0-pre.3 (5479a3a)` | 需要 `-AllowUnverified` |
| 线上守护进程 | `ajs-accountd-61625b0`，早于本版本；本次发布不升级线上主机 | 同上 |

上表的核对命令：

```bash
gh api repos/dwgx/AIJIASU2API/immutable-releases --jq .enabled            # true
gh api repos/dwgx/AIJIASU2API/releases/tags/v0.1.0-pre.3 --jq .immutable  # true
gh api repos/dwgx/AIJIASU2API/releases/tags/v0.1.0-pre.2 --jq .immutable  # false
gh release view v0.1.0-pre.3 --json assets --jq '.assets | length'         # 6
(cd deploy && ./verify-release.sh --tag v0.1.0-pre.3 --require-attestation) # 退出码 0
git show v0.1.0-pre.2:.github/workflows/release.yml | grep -c attest       # 0
```

一句话：`v0.1.0-pre.3` 是第一个带证明且不可变的发布，六个产物与两个镜像摘要均已校验；
`v0.1.0-pre.2` 没有证明、不是不可变发布，也不是线上主机运行的版本。

## 相关文档

- [`DEPLOYMENT.md`](DEPLOYMENT.md) — 本页英文版
- [`docs/OPERATIONS.md`](OPERATIONS.md) — 安装、健康、备份、轮换、升级、回滚
- [`docs/RELEASE.md`](RELEASE.md) — 发布门槛、产物、校验和、打 tag 流程
- [`docs/VERSIONING.md`](VERSIONING.md) — SemVer、tag 与产物、`:latest`
- [`docs/SECURITY-MODEL.md`](SECURITY-MODEL.md) — 在哪里强制了什么，以及不保护什么
- [`deploy/README.md`](../deploy/README.md) — compose 栈、`.env`、TLS、备份
- [`deploy/production/README.md`](../deploy/production/README.md) — 经过评审的 systemd/nginx 套件
- [`deploy/windows/`](../deploy/windows/) — Windows 安装、更新、校验、运行脚本
- [`deploy/verify-release.sh`](../deploy/verify-release.sh) — 发布产物校验器
- [`deploy/update.sh`](../deploy/update.sh) — compose 更新与回滚
