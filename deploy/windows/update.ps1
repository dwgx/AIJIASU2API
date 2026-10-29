#Requires -Version 5.1
<#
.SYNOPSIS
    Update or roll back an installed ajs-accountd on Windows.

.DESCRIPTION
    Update: verifies the release (SHA256SUMS, MANIFEST.txt, stamped -version),
    stops the auto-start task and any running daemon, keeps the current binary
    as ajs-accountd.exe.previous, stages the new binary next to it and moves it
    into place, starts the daemon again and waits for /healthz to report the new
    version. If that check fails, the previous binary is swapped back and
    restarted. The config file (ajs.env) and the state directory are never
    touched.

    -Rollback: swaps ajs-accountd.exe.previous back in, starts it and runs the
    same health check. Nothing is downloaded and no verification is repeated:
    that binary was verified when it was installed.

    The last update is recorded in <InstallDir>\update-state.json.

    Exit codes: 0 updated/rolled back and healthy, 1 update or health failed
    (the previous binary is restored), 2 usage or precondition.

.PARAMETER Tag
    Release tag to install, for example v0.1.0-pre.2. Default: the latest
    release.

.PARAMETER Repo
    OWNER/REPO that owns the release. Default: dwgx/AIJIASU2API.

.PARAMETER InstallDir
    Install directory. Default: C:\ProgramData\AIJIASU2API.

.PARAMETER ServiceName
    Name of the auto-start scheduled task. Default: AJSAccountd.

.PARAMETER RequireAttestation
    Accepted for explicitness: attestations are already required by default.

.PARAMETER AllowUnverified
    The only opt-out from the attestation requirement. Never skips the
    checksum chain.

.PARAMETER Rollback
    Restore ajs-accountd.exe.previous instead of downloading a release.

.PARAMETER TimeoutSec
    Seconds to wait for /healthz after starting. Default 30.

.EXAMPLE
    ./update.ps1 -Tag v0.1.0-pre.2 -AllowUnverified

.EXAMPLE
    ./update.ps1 -Rollback
#>
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'Medium')]
param(
    [string]$Tag,
    [string]$Repo = 'dwgx/AIJIASU2API',
    [string]$InstallDir = 'C:\ProgramData\AIJIASU2API',
    [string]$ServiceName = 'AJSAccountd',
    [switch]$RequireAttestation,
    [switch]$AllowUnverified,
    [switch]$Rollback,
    [int]$TimeoutSec = 30
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

# Set before any failure path runs: Stop-Update removes the download directory
# only after verification produced one.
$script:verifyDir = ''

$ExePath = Join-Path $InstallDir 'ajs-accountd.exe'
$PreviousPath = "$ExePath.previous"
$ConfigPath = Join-Path $InstallDir 'ajs.env'
$StatePath = Join-Path $InstallDir 'update-state.json'
$LogDir = Join-Path $InstallDir 'logs'

function Write-Step {
    param([string]$Message)
    Write-Host "==> $Message"
}

function Write-Warn {
    param([string]$Message)
    Write-Host "warning: $Message" -ForegroundColor Yellow
}

function Stop-Update {
    param([string]$Message, [int]$Code = 1)
    Write-Host "update: $Message" -ForegroundColor Red
    if ($script:verifyDir -and (Test-Path -LiteralPath $script:verifyDir)) {
        Remove-Item -LiteralPath $script:verifyDir -Recurse -Force -ErrorAction SilentlyContinue
    }
    exit $Code
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

function Get-BinaryVersion {
    param([string]$Path)
    # PowerShell refuses to launch a file whose extension is not executable, so
    # ajs-accountd.exe.previous is probed through a temporary .exe copy.
    $probe = $Path
    $tempCopy = $null
    if ([System.IO.Path]::GetExtension($Path) -ne '.exe') {
        $tempCopy = Join-Path ([System.IO.Path]::GetTempPath()) ('ajs-probe-' + [guid]::NewGuid().ToString('N') + '.exe')
        Copy-Item -LiteralPath $Path -Destination $tempCopy -Force
        $probe = $tempCopy
    }
    try {
        $line = (& $probe -version 2>&1 | Out-String).Trim()
        if ($LASTEXITCODE -eq 0 -and $line -match '^ajs-accountd\s+(\S+)\s+\(([^)]*)\)') {
            return [pscustomobject]@{ Version = $Matches[1]; Commit = $Matches[2]; Line = $line }
        }
    }
    catch {
        # Not executable here.
    }
    finally {
        if ($tempCopy -and (Test-Path -LiteralPath $tempCopy)) {
            Remove-Item -LiteralPath $tempCopy -Force -ErrorAction SilentlyContinue
        }
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
    Stop-Update "a process from $Path is still running; stop it and re-run" 1
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

function Test-TaskRegistered {
    param([string]$Name)
    $task = Get-ScheduledTask -TaskName $Name -ErrorAction SilentlyContinue
    return [bool]$task
}

function Start-Daemon {
    param([string]$TaskName, [string]$HealthUrl, [string]$ExpectedVersion, [int]$TimeoutSec)
    Start-ScheduledTask -TaskName $TaskName
    return Wait-Health -Url $HealthUrl -TimeoutSec $TimeoutSec -ExpectedVersion $ExpectedVersion
}

function Write-UpdateState {
    param([string]$Tag, [string]$Version, [string]$PreviousVersion, [string]$Sha256, [string]$PreviousSha256, [string]$HealthUrl, [bool]$RolledBack)
    $state = [ordered]@{
        updated_at       = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
        tag              = $Tag
        version          = $Version
        previous_version = $PreviousVersion
        sha256           = $Sha256
        previous_sha256  = $PreviousSha256
        health_url       = $HealthUrl
        rolled_back      = $RolledBack
    }
    $state | ConvertTo-Json | Set-Content -LiteralPath $StatePath -Encoding ASCII
}

function Show-LogTail {
    param([int]$Lines = 20)
    $log = Get-ChildItem -LiteralPath $LogDir -Filter '*.log' -ErrorAction SilentlyContinue |
        Sort-Object LastWriteTime -Descending |
        Select-Object -First 1
    if ($log) {
        Write-Host "--- $($log.FullName) (last $Lines lines) ---"
        Get-Content -LiteralPath $log.FullName -Tail $Lines | ForEach-Object { Write-Host $_ }
        Write-Host '--- end of log ---'
    }
}

# --- preconditions ---------------------------------------------------------

if (-not (Test-Path -LiteralPath $ExePath)) {
    Stop-Update "no installation at $ExePath; run install.ps1 first" 2
}
if (-not (Test-Path -LiteralPath $ConfigPath)) {
    Stop-Update "no config at $ConfigPath; run install.ps1 first" 2
}

$cfg = Read-EnvFile -Path $ConfigPath
$listen = if ($cfg.ContainsKey('AJS_LISTEN')) { $cfg['AJS_LISTEN'] } else { '127.0.0.1:2261' }
$stateDir = if ($cfg.ContainsKey('AJS_STATE_DIR')) { $cfg['AJS_STATE_DIR'] } else { '(daemon default)' }
$healthUrl = Get-HealthUrl -Listen $listen
$hasTask = Test-TaskRegistered -Name $ServiceName

if ($WhatIfPreference) {
    Write-Host 'update: -WhatIf, nothing is written and no task is changed'
    if ($Rollback) {
        Write-Host "  rollback:   $PreviousPath -> $ExePath"
    }
    else {
        Write-Host "  tag:        $(if ($Tag) { $Tag } else { '(latest release)' })"
        Write-Host "  replace:    $ExePath (previous kept as $PreviousPath)"
    }
    Write-Host "  config:     $ConfigPath (unchanged)"
    Write-Host "  state:      $stateDir (unchanged)"
    Write-Host "  health:     $healthUrl"
    if ($hasTask) { Write-Host "  auto-start: restart scheduled task '$ServiceName'" }
    else { Write-Host '  auto-start: none registered; the binary is replaced but not started' }
    exit 0
}

if (-not $hasTask) {
    $running = Get-Process -Name 'ajs-accountd' -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $ExePath }
    if ($running) {
        Stop-Update "a console instance from $ExePath is running and no auto-start task '$ServiceName' is registered; stop it first, or install auto-start with install.ps1 -Force" 2
    }
}

$current = Get-BinaryVersion -Path $ExePath
if (-not $current) {
    Write-Warn "could not read the current version from $ExePath; continuing"
}
$currentVersion = if ($current) { $current.Version } else { $null }
$currentLabel = if ($current) { $current.Version } else { 'unknown' }

# --- rollback --------------------------------------------------------------

if ($Rollback) {
    if (-not (Test-Path -LiteralPath $PreviousPath)) {
        Stop-Update "no $PreviousPath to roll back to" 2
    }

    # The backup is only a rollback target if it is still the binary the last
    # update recorded. Verify before executing or swapping it.
    $backupSha = (Get-FileHash -LiteralPath $PreviousPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $recorded = @()
    if (Test-Path -LiteralPath $StatePath) {
        try {
            $state = Get-Content -LiteralPath $StatePath -Raw | ConvertFrom-Json
            $field = if ($state.rolled_back) { 'sha256' } else { 'previous_sha256' }
            if ($state.PSObject.Properties.Name -contains $field -and $state.$field) {
                $recorded = @($state.$field.ToString().ToLowerInvariant())
            }
        }
        catch {
            Write-Warn "could not read $StatePath; the backup hash cannot be checked"
        }
    }
    if ($recorded.Count -gt 0) {
        if ($recorded -notcontains $backupSha) {
            Stop-Update "$PreviousPath has sha256 $backupSha, but $StatePath records $($recorded -join ', '); refusing to swap an unrecorded binary back in" 1
        }
        Write-Host "    backup sha256 matches $StatePath ($backupSha)"
    }
    else {
        Write-Warn "no recorded sha256 for $PreviousPath; the backup cannot be verified against update-state.json"
    }

    $target = Get-BinaryVersion -Path $PreviousPath
    if (-not $target) {
        Stop-Update "the previous binary at $PreviousPath does not run here; refusing to swap it in" 1
    }
    Write-Step "rolling back to $($target.Version) ($($target.Commit))"
    if ($hasTask) { Stop-ScheduledTask -TaskName $ServiceName -ErrorAction SilentlyContinue }
    Stop-AjsProcesses -Path $ExePath

    $staging = "$ExePath.new"
    Clear-ReparseTarget -Path $staging
    Copy-Item -LiteralPath $PreviousPath -Destination $staging -Force
    Clear-ReparseTarget -Path $ExePath
    Move-Item -LiteralPath $staging -Destination $ExePath -Force

    if ($hasTask) {
        $health = Start-Daemon -TaskName $ServiceName -HealthUrl $healthUrl -ExpectedVersion $target.Version -TimeoutSec $TimeoutSec
        if (-not $health) {
            Show-LogTail
            Stop-Update "the rolled-back binary did not answer $healthUrl with $($target.Version); it stays in place" 1
        }
        Write-Host "    healthz: $($health | ConvertTo-Json -Compress)"
    }
    Write-UpdateState -Tag '(rollback)' -Version $target.Version -PreviousVersion $currentLabel `
        -Sha256 (Get-FileHash -LiteralPath $ExePath -Algorithm SHA256).Hash.ToLowerInvariant() `
        -PreviousSha256 (Get-FileHash -LiteralPath $PreviousPath -Algorithm SHA256).Hash.ToLowerInvariant() `
        -HealthUrl $healthUrl -RolledBack $true

    Write-Host ''
    Write-Host "rolled back to ajs-accountd $($target.Version)"
    Write-Host "  binary:  $ExePath"
    Write-Host "  config:  $ConfigPath (unchanged)"
    Write-Host "  state:   $stateDir (unchanged)"
    exit 0
}

# --- update ----------------------------------------------------------------

$verifyScript = Join-Path $PSScriptRoot 'verify-release.ps1'
if (-not (Test-Path -LiteralPath $verifyScript)) {
    Stop-Update "verify-release.ps1 not found next to update.ps1 ($PSScriptRoot)" 2
}

$verifyArgs = @{ Repo = $Repo; PassThru = $true }
if ($Tag) { $verifyArgs.Tag = $Tag }
if ($RequireAttestation) { $verifyArgs.RequireAttestation = $true }
if ($AllowUnverified) { $verifyArgs.AllowUnverified = $true }

Write-Step 'verifying the release'
$verified = & $verifyScript @verifyArgs
if ($LASTEXITCODE -ne 0 -or -not $verified) {
    $code = if ($LASTEXITCODE -ne 0) { $LASTEXITCODE } else { 1 }
    Stop-Update 'release verification failed; the installed binary was not touched' $code
}
$script:verifyDir = $verified.Dir

if ($current -and $current.Version -eq $verified.Version) {
    Write-Warn "the installed binary already reports $($verified.Version); reinstalling it anyway"
}

Write-Step "updating $($current.Version) -> $($verified.Version)"
if ($hasTask) { Stop-ScheduledTask -TaskName $ServiceName -ErrorAction SilentlyContinue }
Stop-AjsProcesses -Path $ExePath

Clear-ReparseTarget -Path $PreviousPath
Copy-Item -LiteralPath $ExePath -Destination $PreviousPath -Force
$previousSha = (Get-FileHash -LiteralPath $PreviousPath -Algorithm SHA256).Hash.ToLowerInvariant()
Write-Host "    previous binary kept at $PreviousPath (sha256 $previousSha)"

$staging = "$ExePath.new"
Clear-ReparseTarget -Path $staging
Copy-Item -LiteralPath $verified.ExePath -Destination $staging -Force
$stagedHash = (Get-FileHash -LiteralPath $staging -Algorithm SHA256).Hash.ToLowerInvariant()
if ($stagedHash -ne $verified.Sha256) {
    Remove-Item -LiteralPath $staging -Force -ErrorAction SilentlyContinue
    Stop-Update 'the staged copy does not match the verified sha256; the installed binary was not replaced' 1
}
Clear-ReparseTarget -Path $ExePath
Move-Item -LiteralPath $staging -Destination $ExePath -Force

# The auto-start task runs <InstallDir>\run.ps1, so keep it in step with the
# scripts that shipped this update.
$runnerSource = Join-Path $PSScriptRoot 'run.ps1'
if (Test-Path -LiteralPath $runnerSource) {
    $runnerPath = Join-Path $InstallDir 'run.ps1'
    Clear-ReparseTarget -Path $runnerPath
    Copy-Item -LiteralPath $runnerSource -Destination $runnerPath -Force
}

$health = $null
if ($hasTask) {
    Write-Step "waiting for $healthUrl"
    $health = Start-Daemon -TaskName $ServiceName -HealthUrl $healthUrl -ExpectedVersion $verified.Version -TimeoutSec $TimeoutSec
}

if ($hasTask -and -not $health) {
    Write-Warn "the new binary did not become healthy; restoring the previous binary"
    Show-LogTail
    if ($hasTask) { Stop-ScheduledTask -TaskName $ServiceName -ErrorAction SilentlyContinue }
    Stop-AjsProcesses -Path $ExePath
    Clear-ReparseTarget -Path "$ExePath.new"
    Copy-Item -LiteralPath $PreviousPath -Destination "$ExePath.new" -Force
    Clear-ReparseTarget -Path $ExePath
    Move-Item -LiteralPath "$ExePath.new" -Destination $ExePath -Force
    $restored = $null
    if ($hasTask) {
        $restored = Start-Daemon -TaskName $ServiceName -HealthUrl $healthUrl -ExpectedVersion $currentVersion -TimeoutSec $TimeoutSec
    }
    if ($restored) {
        Write-Host "    restored: $($restored | ConvertTo-Json -Compress)"
        Write-UpdateState -Tag $verified.Tag -Version $currentLabel -PreviousVersion $verified.Version `
            -Sha256 $previousSha -PreviousSha256 $verified.Sha256 `
            -HealthUrl $healthUrl -RolledBack $true
        Stop-Update "update to $($verified.Version) failed the health check; the previous binary is back and healthy" 1
    }
    Stop-Update "update to $($verified.Version) failed and the previous binary did not come back healthy either; inspect $LogDir" 1
}

if ($health) {
    Write-Host "    healthz: $($health | ConvertTo-Json -Compress)"
}

Write-UpdateState -Tag $verified.Tag -Version $verified.Version -PreviousVersion $currentLabel `
    -Sha256 $verified.Sha256 -PreviousSha256 $previousSha `
    -HealthUrl $healthUrl -RolledBack $false

Write-Host ''
Write-Host "updated ajs-accountd to $($verified.Version) ($($verified.Commit))"
Write-Host "  binary:   $ExePath (sha256 $($verified.Sha256))"
Write-Host "  previous: $PreviousPath (restore with: ./update.ps1 -Rollback)"
Write-Host "  config:   $ConfigPath (unchanged)"
Write-Host "  state:    $stateDir (unchanged)"
if (-not $hasTask) {
    Write-Host '  auto-start: none registered; start the new binary with run.ps1 -FromConfig'
}

if ($verified.Dir -and (Test-Path -LiteralPath $verified.Dir)) {
    Remove-Item -LiteralPath $verified.Dir -Recurse -Force -ErrorAction SilentlyContinue
}

exit 0
