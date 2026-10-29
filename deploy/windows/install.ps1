#Requires -Version 5.1
<#
.SYNOPSIS
    Install ajs-accountd on Windows from a verified GitHub release.

.DESCRIPTION
    Verifies the release first (verify-release.ps1: SHA256SUMS, MANIFEST.txt and
    the stamped -version), then installs:

      <InstallDir>\ajs-accountd.exe      the verified binary
      <InstallDir>\run.ps1               the launcher the auto-start task uses
      <InstallDir>\ajs.env               generated once; never overwritten
      <InstallDir>\data\                 state, kept across installs and updates
      <InstallDir>\logs\                 one log pair per daemon start

    The binary is staged next to its destination and moved into place, so a
    failed copy cannot leave a half-written executable. The config file carries
    the admin bearer and the at-rest master key and is restricted to SYSTEM,
    Administrators and the installing account.

    Auto-start: a scheduled task (default name AJSAccountd) that runs
    run.ps1 -FromConfig at startup as SYSTEM. A Windows service is deliberately
    not used: ajs-accountd does not implement the SCM dispatcher
    (StartServiceCtrlDispatcher), so `sc.exe create` with this binary fails with
    error 1053 and the SCM terminates it. See README.md.

    Exit codes: 0 installed and healthy, 1 verification or health failed,
    2 usage or precondition.

.PARAMETER Tag
    Release tag, for example v0.1.0-pre.2. Default: the latest release.

.PARAMETER Repo
    OWNER/REPO that owns the release. Default: dwgx/AIJIASU2API.

.PARAMETER InstallDir
    Install directory. Default: C:\ProgramData\AIJIASU2API.

.PARAMETER StateDir
    State directory used when the config file is first generated. Default:
    <InstallDir>\data.

.PARAMETER Listen
    Listener written into a new config. Default: 127.0.0.1:2261.

.PARAMETER ServiceName
    Name of the auto-start scheduled task. Default: AJSAccountd.

.PARAMETER NoService
    Do not register auto-start. The files are installed and the daemon is left
    stopped; start it with run.ps1 -FromConfig.

.PARAMETER RequireAttestation
    Accepted for explicitness: attestations are already required by default.

.PARAMETER AllowUnverified
    The only opt-out from the attestation requirement. Never skips the
    checksum chain.

.PARAMETER Force
    Replace an existing installation in place (stops it first). The config
    file is still never overwritten.

.PARAMETER TimeoutSec
    Seconds to wait for /healthz after starting. Default 30.

.EXAMPLE
    ./install.ps1 -Tag v0.1.0-pre.2 -AllowUnverified

.EXAMPLE
    ./install.ps1 -Tag v0.1.0-pre.2 -InstallDir D:\ajs -Listen 127.0.0.1:2261
#>
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'Medium')]
param(
    [string]$Tag,
    [string]$Repo = 'dwgx/AIJIASU2API',
    [string]$InstallDir = 'C:\ProgramData\AIJIASU2API',
    [string]$StateDir,
    [string]$Listen = '127.0.0.1:2261',
    [string]$ServiceName = 'AJSAccountd',
    [switch]$NoService,
    [switch]$RequireAttestation,
    [switch]$AllowUnverified,
    [switch]$Force,
    [int]$TimeoutSec = 30
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

# Set before any failure path runs: Stop-Install removes the download directory
# only after verification produced one.
$script:verifyDir = ''

$ExePath = Join-Path $InstallDir 'ajs-accountd.exe'
$ConfigPath = Join-Path $InstallDir 'ajs.env'
$RunnerPath = Join-Path $InstallDir 'run.ps1'
$LogDir = Join-Path $InstallDir 'logs'
if (-not $StateDir) { $StateDir = Join-Path $InstallDir 'data' }

function Write-Step {
    param([string]$Message)
    Write-Host "==> $Message"
}

function Write-Warn {
    param([string]$Message)
    Write-Host "warning: $Message" -ForegroundColor Yellow
}

function Stop-Install {
    param([string]$Message, [int]$Code = 1)
    Write-Host "install: $Message" -ForegroundColor Red
    if ($script:verifyDir -and (Test-Path -LiteralPath $script:verifyDir)) {
        Remove-Item -LiteralPath $script:verifyDir -Recurse -Force -ErrorAction SilentlyContinue
    }
    exit $Code
}

function Test-Admin {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function New-RandomHex {
    param([int]$Bytes = 32)
    $buffer = New-Object byte[] $Bytes
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($buffer) } finally { $rng.Dispose() }
    return -join ($buffer | ForEach-Object { $_.ToString('x2') })
}

function Read-EnvFile {
    param([string]$Path)
    $cfg = @{}
    foreach ($line in Get-Content -LiteralPath $Path) {
        $text = $line.Trim()
        if (-not $text -or $text.StartsWith('#')) { continue }
        $split = $text.IndexOf('=')
        if ($split -lt 1) { continue }
        $cfg[$text.Substring(0, $split).Trim()] = $text.Substring($split + 1).Trim()
    }
    return $cfg
}

function Get-RestrictedSids {
    return @('S-1-5-18', 'S-1-5-32-544', [Security.Principal.WindowsIdentity]::GetCurrent().User.Value)
}

function Test-ProtectedPath {
    param([string]$Path)
    $allowed = Get-RestrictedSids
    $acl = Get-Acl -LiteralPath $Path
    if (-not $acl.AreAccessRulesProtected) { return $false }
    $seen = @()
    foreach ($rule in @($acl.Access)) {
        if ($rule.AccessControlType -ne 'Allow') { return $false }
        $sid = $rule.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value
        if ($allowed -notcontains $sid) { return $false }
        $seen += $sid
    }
    foreach ($sid in $allowed) {
        if ($seen -notcontains $sid) { return $false }
    }
    return $true
}

function Protect-Path {
    param([string]$Path, [switch]$Directory)
    if (Test-ProtectedPath -Path $Path) { return }
    # Set-Acl on an object whose DACL is already protected needs
    # SeSecurityPrivilege, so a path we do not own the DACL of is refused
    # instead of half-written.
    if ((Get-Acl -LiteralPath $Path).AreAccessRulesProtected) {
        throw "$Path already has a protected DACL that is not the SYSTEM/Administrators/installer one; refusing to install over it"
    }
    $acl = if ($Directory) {
        New-Object System.Security.AccessControl.DirectorySecurity
    }
    else {
        New-Object System.Security.AccessControl.FileSecurity
    }
    $acl.SetAccessRuleProtection($true, $false)
    $inheritance = if ($Directory) { 'ContainerInherit, ObjectInherit' } else { 'None' }
    foreach ($sid in (Get-RestrictedSids)) {
        $identity = New-Object System.Security.Principal.SecurityIdentifier($sid)
        $rule = New-Object System.Security.AccessControl.FileSystemAccessRule($identity, 'FullControl', $inheritance, 'None', 'Allow')
        [void]$acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
    if (-not (Test-ProtectedPath -Path $Path)) {
        throw "the DACL on $Path is still not the restricted one after Set-Acl"
    }
}

function Assert-InstallDir {
    param([string]$Path)
    $item = Get-Item -LiteralPath $Path -Force
    if ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) {
        throw "$Path is a reparse point (symlink or junction); install into a real directory"
    }
    if (-not $item.PSIsContainer) {
        throw "$Path exists and is not a directory"
    }
    $owner = (Get-Acl -LiteralPath $Path).GetOwner([System.Security.Principal.SecurityIdentifier]).Value
    if ((Get-RestrictedSids) -notcontains $owner) {
        throw "$Path is owned by $owner, which is not SYSTEM, Administrators or the installing account; refusing to write a SYSTEM auto-start into a directory another account controls"
    }
}

function New-ProtectedTextFile {
    param([string]$Path, [string[]]$Lines)
    # Create the file empty, lock its DACL down, and only then write the
    # secrets into it.
    $stream = [System.IO.File]::Open($Path, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
    $stream.Close()
    Protect-Path -Path $Path
    Set-Content -LiteralPath $Path -Value $Lines -Encoding ASCII
    if (-not (Test-ProtectedPath -Path $Path)) {
        throw "the config file $Path is not restricted after writing it"
    }
}

function Clear-ReparseTarget {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) { return }
    $item = Get-Item -LiteralPath $Path -Force
    if ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) {
        # A pre-created link would redirect the write to another location.
        Remove-Item -LiteralPath $Path -Force
        Write-Warn "removed the reparse point at $Path before writing"
    }
}

function Get-HealthUrl {
    param([string]$Listen)
    $hostPart = $Listen
    $portPart = ''
    $split = $Listen.LastIndexOf(':')
    if ($split -ge 0) {
        $hostPart = $Listen.Substring(0, $split)
        $portPart = $Listen.Substring($split + 1)
    }
    if (-not $hostPart) { $hostPart = '127.0.0.1' }
    if ($hostPart -eq '0.0.0.0' -or $hostPart -eq '::' -or $hostPart -eq '[::]') { $hostPart = '127.0.0.1' }
    if ($hostPart.StartsWith('[') -and $hostPart.EndsWith(']')) { $hostPart = $hostPart.Substring(1, $hostPart.Length - 2) }
    if ($hostPart.Contains(':')) { $hostPart = "[$hostPart]" }
    return "http://${hostPart}:${portPart}/healthz"
}

function Wait-Health {
    param([string]$Url, [int]$TimeoutSec, [string]$ExpectedVersion)
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        try {
            $resp = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 2
            if ($resp.StatusCode -eq 200) {
                $json = $resp.Content | ConvertFrom-Json
                if (-not $ExpectedVersion -or $json.version -eq $ExpectedVersion) {
                    return $json
                }
            }
        }
        catch {
            # Not listening yet.
        }
        Start-Sleep -Milliseconds 300
    }
    return $null
}

function Stop-AjsProcesses {
    param([string]$Path)
    Get-Process -Name 'ajs-accountd' -ErrorAction SilentlyContinue |
        Where-Object { $_.Path -eq $Path } |
        ForEach-Object { Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue }
    $deadline = (Get-Date).AddSeconds(15)
    while ((Get-Date) -lt $deadline) {
        $running = Get-Process -Name 'ajs-accountd' -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $Path }
        if (-not $running) { return }
        Start-Sleep -Milliseconds 300
    }
    Write-Warn "a process from $Path is still running"
}

if ($WhatIfPreference) {
    Write-Host 'install: -WhatIf, nothing is written and no task is registered'
    Write-Host "  tag:         $(if ($Tag) { $Tag } else { '(latest release)' })"
    Write-Host "  install dir: $InstallDir"
    Write-Host "  binary:      $ExePath"
    Write-Host "  launcher:    $RunnerPath"
    Write-Host "  config:      $ConfigPath (created only when absent; never overwritten)"
    Write-Host "  state dir:   $StateDir"
    Write-Host "  listen:      $Listen"
    if ($NoService) {
        Write-Host '  auto-start:  none (-NoService)'
    }
    else {
        Write-Host "  auto-start:  scheduled task '$ServiceName' (at startup, SYSTEM, highest, no time limit)"
    }
    exit 0
}

# --- 1. verify before anything touches the host ----------------------------

$verifyScript = Join-Path $PSScriptRoot 'verify-release.ps1'
if (-not (Test-Path -LiteralPath $verifyScript)) {
    Stop-Install "verify-release.ps1 not found next to install.ps1 ($PSScriptRoot)" 2
}
$runnerSource = Join-Path $PSScriptRoot 'run.ps1'
if (-not (Test-Path -LiteralPath $runnerSource)) {
    Stop-Install "run.ps1 not found next to install.ps1 ($PSScriptRoot)" 2
}

$verifyArgs = @{ Repo = $Repo; PassThru = $true }
if ($Tag) { $verifyArgs.Tag = $Tag }
if ($RequireAttestation) { $verifyArgs.RequireAttestation = $true }
if ($AllowUnverified) { $verifyArgs.AllowUnverified = $true }

Write-Step 'verifying the release'
$verified = & $verifyScript @verifyArgs
if ($LASTEXITCODE -ne 0 -or -not $verified) {
    $code = if ($LASTEXITCODE -ne 0) { $LASTEXITCODE } else { 1 }
    Stop-Install 'release verification failed; nothing was installed' $code
}
$script:verifyDir = $verified.Dir

# --- 2. preconditions ------------------------------------------------------

if ((Test-Path -LiteralPath $ExePath) -and -not $Force) {
    Stop-Install "$ExePath already exists; use update.ps1 to replace it, or pass -Force" 2
}
if (-not $NoService -and -not (Test-Admin)) {
    Stop-Install 'registering the auto-start task needs an elevated PowerShell; re-run as administrator or pass -NoService' 2
}

$cfg = $null
$effectiveListen = $Listen
$effectiveState = $StateDir
if (Test-Path -LiteralPath $ConfigPath) {
    $cfg = Read-EnvFile -Path $ConfigPath
    if (-not $cfg.ContainsKey('AJS_ADMIN_TOKEN') -and $cfg['AJS_ALLOW_INSECURE'] -ne '1') {
        Stop-Install "existing $ConfigPath has neither AJS_ADMIN_TOKEN nor AJS_ALLOW_INSECURE=1, so the daemon would refuse to start. Fix that file by hand; this script never rewrites it." 2
    }
    if ($cfg.ContainsKey('AJS_LISTEN')) { $effectiveListen = $cfg['AJS_LISTEN'] }
    if ($cfg.ContainsKey('AJS_STATE_DIR')) { $effectiveState = $cfg['AJS_STATE_DIR'] }
    Write-Host "    keeping existing config $ConfigPath (listen $effectiveListen)"
}
# A service runs with its working directory outside the install tree, so a
# relative state path would land somewhere unexpected. Anchor it.
if ($effectiveState -and -not [System.IO.Path]::IsPathRooted($effectiveState)) {
    $effectiveState = Join-Path $InstallDir $effectiveState
}

# --- 3. files --------------------------------------------------------------

Write-Step "installing $($verified.Version) into $InstallDir"
try {
    if (Test-Path -LiteralPath $InstallDir) {
        Assert-InstallDir -Path $InstallDir
    }
    else {
        New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    }
    # Lock the directory down before anything is written into it: it holds
    # run.ps1, which the SYSTEM auto-start task executes at boot.
    Protect-Path -Path $InstallDir -Directory
}
catch {
    Stop-Install $_.Exception.Message 1
}

if (-not (Test-Path -LiteralPath $effectiveState)) {
    New-Item -ItemType Directory -Path $effectiveState -Force | Out-Null
}
try {
    $stateItem = Get-Item -LiteralPath $effectiveState -Force
    if ($stateItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) {
        throw "$effectiveState is a reparse point (symlink or junction); point AJS_STATE_DIR at a real directory"
    }
    Protect-Path -Path $effectiveState -Directory
}
catch {
    Stop-Install "cannot restrict the state directory: $($_.Exception.Message)" 1
}

if (-not $cfg) {
    $lines = @(
        '# ajs-accountd configuration generated by deploy/windows/install.ps1.',
        '# Holds the admin bearer and the at-rest master key: never commit this file,',
        '# and keep a copy of AJS_MASTER_KEY -- losing it makes sealed account',
        '# passwords unrecoverable.',
        "# generated: $(Get-Date -Format 'yyyy-MM-ddTHH:mm:ssZ')",
        "AJS_LISTEN=$effectiveListen",
        "AJS_STATE_DIR=$effectiveState",
        'AJS_LOG_LEVEL=info',
        "AJS_ADMIN_TOKEN=$(New-RandomHex 32)",
        "AJS_MASTER_KEY=$(New-RandomHex 32)",
        'AJS_TENANT_PORTAL=0',
        'AJS_TRUSTED_PROXIES=127.0.0.1,::1'
    )
    try {
        New-ProtectedTextFile -Path $ConfigPath -Lines $lines
    }
    catch {
        Stop-Install "cannot write a config file with a restricted DACL: $($_.Exception.Message)" 1
    }
    Write-Host "    wrote $ConfigPath (SYSTEM/Administrators/installer only)"
}

if (Test-Path -LiteralPath $ExePath) {
    if (-not $NoService) {
        Stop-ScheduledTask -TaskName $ServiceName -ErrorAction SilentlyContinue
    }
    Stop-AjsProcesses -Path $ExePath
}

$staging = "$ExePath.new"
Clear-ReparseTarget -Path $staging
Copy-Item -LiteralPath $verified.ExePath -Destination $staging -Force
$stagedHash = (Get-FileHash -LiteralPath $staging -Algorithm SHA256).Hash.ToLowerInvariant()
if ($stagedHash -ne $verified.Sha256) {
    Remove-Item -LiteralPath $staging -Force -ErrorAction SilentlyContinue
    Stop-Install 'the staged copy does not match the verified sha256; nothing was replaced' 1
}
Clear-ReparseTarget -Path $ExePath
Move-Item -LiteralPath $staging -Destination $ExePath -Force
Clear-ReparseTarget -Path $RunnerPath
Copy-Item -LiteralPath $runnerSource -Destination $RunnerPath -Force
Write-Host "    binary $ExePath sha256 $($verified.Sha256)"

# --- 4. auto-start ---------------------------------------------------------

$healthUrl = Get-HealthUrl -Listen $effectiveListen
if ($NoService) {
    Write-Step 'skipping auto-start (-NoService)'
}
else {
    Write-Step "registering the auto-start task '$ServiceName'"
    $psExe = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
    if (-not (Test-Path -LiteralPath $psExe)) {
        Stop-Install "powershell.exe not found at $psExe" 2
    }
    $argument = '-NoProfile -ExecutionPolicy Bypass -File "' + $RunnerPath + '" -FromConfig'
    $action = New-ScheduledTaskAction -Execute $psExe -Argument $argument
    $trigger = New-ScheduledTaskTrigger -AtStartup
    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    $settings = New-ScheduledTaskSettingsSet `
        -AllowStartIfOnBatteries `
        -DontStopIfGoingOnBatteries `
        -ExecutionTimeLimit ([TimeSpan]::Zero) `
        -RestartCount 3 `
        -RestartInterval (New-TimeSpan -Minutes 1) `
        -StartWhenAvailable
    Register-ScheduledTask -TaskName $ServiceName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
    Start-ScheduledTask -TaskName $ServiceName

    Write-Step "waiting for $healthUrl"
    $health = Wait-Health -Url $healthUrl -TimeoutSec $TimeoutSec -ExpectedVersion $verified.Version
    if (-not $health) {
        Stop-ScheduledTask -TaskName $ServiceName -ErrorAction SilentlyContinue
        Stop-AjsProcesses -Path $ExePath
        Unregister-ScheduledTask -TaskName $ServiceName -Confirm:$false -ErrorAction SilentlyContinue
        Write-Warn "removed the scheduled task '$ServiceName' after a failed health check"
        $logs = Get-ChildItem -LiteralPath $LogDir -Filter '*.log' -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending | Select-Object -First 1
        if ($logs) { Write-Host "--- $($logs.FullName) (last 20 lines) ---"; Get-Content -LiteralPath $logs.FullName -Tail 20 | ForEach-Object { Write-Host $_ } }
        Stop-Install "the daemon did not answer $healthUrl with version $($verified.Version) within ${TimeoutSec}s; files and config stay in $InstallDir" 1
    }
    Write-Host "    healthz: $($health | ConvertTo-Json -Compress)"
}

# --- 5. summary ------------------------------------------------------------

Write-Host ''
Write-Host "installed ajs-accountd $($verified.Version) ($($verified.Commit))"
Write-Host "  binary:     $ExePath"
Write-Host "  config:     $ConfigPath (read AJS_ADMIN_TOKEN from it; it is never overwritten)"
Write-Host "  state:      $effectiveState (kept across updates)"
Write-Host "  health:     $healthUrl"
if ($NoService) {
    Write-Host "  auto-start: none; start it with: `"$RunnerPath`" -FromConfig"
}
else {
    Write-Host "  auto-start: scheduled task '$ServiceName' (at startup, SYSTEM)"
    Write-Host "  stop:       Stop-ScheduledTask -TaskName $ServiceName"
}

if ($verified.Dir -and (Test-Path -LiteralPath $verified.Dir)) {
    Remove-Item -LiteralPath $verified.Dir -Recurse -Force -ErrorAction SilentlyContinue
}

exit 0
