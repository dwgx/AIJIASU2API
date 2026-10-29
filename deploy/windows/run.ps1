#Requires -Version 5.1
<#
.SYNOPSIS
    Run ajs-accountd in the current console: a throwaway smoke run, or the
    installed configuration used by the auto-start task.

.DESCRIPTION
    Two modes:

      Smoke / lab (default): no config file is read. The listener is loopback
      on a free port (or -Port), AJS_STATE_DIR is a fresh temporary directory,
      and AJS_ALLOW_INSECURE=1 is set unless -Token is supplied, so the daemon
      needs no operator setup. -Smoke starts the daemon, waits for /healthz,
      prints the answer and stops it again; without -Smoke the daemon keeps
      running until it exits.

      -FromConfig: reads KEY=VALUE lines from <InstallDir>\ajs.env (or
      -ConfigPath) and runs the installed binary with exactly that
      environment. This is what the auto-start task invokes; the only thing it
      writes is a log under <InstallDir>\logs.

    No service, task or machine-wide setting is created or changed here.

    Exit codes: 0 the daemon answered /healthz (smoke) or exited 0, 1 the
    daemon failed to start or answer /healthz, 2 usage or precondition.

.PARAMETER Exe
    Binary to run. Default: <InstallDir>\ajs-accountd.exe.

.PARAMETER InstallDir
    Install directory. Default: C:\ProgramData\AIJIASU2API.

.PARAMETER FromConfig
    Load the installed ajs.env instead of the throwaway smoke defaults.

.PARAMETER ConfigPath
    Config file for -FromConfig. Default: <InstallDir>\ajs.env.

.PARAMETER Port
    Loopback port for a smoke run. Default 0: pick a free port.

.PARAMETER StateDir
    State directory. Default: a fresh temporary directory (smoke), or the
    AJS_STATE_DIR value from the config (-FromConfig).

.PARAMETER Token
    Admin bearer for the run. Without it a smoke run sets
    AJS_ALLOW_INSECURE=1; with it the daemon requires the bearer.

.PARAMETER Smoke
    Start, wait for /healthz, print it, stop, exit.

.PARAMETER TimeoutSec
    Seconds to wait for /healthz. Default 30.

.PARAMETER KeepState
    Keep the temporary state directory and logs of a smoke run.

.PARAMETER StreamLog
    Do not redirect the daemon output to a log file; let it write to this
    console instead. Scripted runs should leave this off.

.EXAMPLE
    ./run.ps1 -Exe .\ajs-accountd.exe -Smoke

.EXAMPLE
    ./run.ps1 -FromConfig -StreamLog
#>
[CmdletBinding()]
param(
    [string]$Exe,
    [string]$InstallDir = 'C:\ProgramData\AIJIASU2API',
    [switch]$FromConfig,
    [string]$ConfigPath,
    [int]$Port = 0,
    [string]$StateDir,
    [string]$Token,
    [switch]$Smoke,
    [int]$TimeoutSec = 30,
    [switch]$KeepState,
    [switch]$StreamLog
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

function Write-Step {
    param([string]$Message)
    Write-Host "==> $Message"
}

function Write-Warn {
    param([string]$Message)
    Write-Host "warning: $Message" -ForegroundColor Yellow
}

function Stop-Run {
    param([string]$Message, [int]$Code = 1)
    Write-Host "run: $Message" -ForegroundColor Red
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

function Get-FreeLoopbackPort {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    $listener.Stop()
    return $port
}

function Wait-Health {
    param([string]$Url, [int]$TimeoutSec, [string]$ExpectedVersion, [System.Diagnostics.Process]$Process)
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        if ($Process -and $Process.HasExited) {
            # The daemon gave up (bad flag, port in use); stop waiting.
            return $null
        }
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
            # Not listening yet, or the body is not JSON yet.
        }
        Start-Sleep -Milliseconds 300
    }
    return $null
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

function Show-LogTail {
    param([string]$Path, [int]$Lines = 20)
    if ($Path -and (Test-Path -LiteralPath $Path)) {
        $tail = Get-Content -LiteralPath $Path -Tail $Lines -ErrorAction SilentlyContinue
        if ($tail) {
            Write-Host "--- $Path (last $Lines lines) ---"
            $tail | ForEach-Object { Write-Host $_ }
            Write-Host '--- end of log ---'
        }
    }
}

# --- resolve the binary and the environment --------------------------------

if (-not $Exe) { $Exe = Join-Path $InstallDir 'ajs-accountd.exe' }
if (-not (Test-Path -LiteralPath $Exe)) {
    Stop-Run "binary not found: $Exe (install first with install.ps1, or pass -Exe)" 2
}
$Exe = (Resolve-Path -LiteralPath $Exe).Path

# Never let a stale value from the calling shell decide how the daemon binds.
$managed = @('AJS_LISTEN', 'AJS_STATE_DIR', 'AJS_ADMIN_TOKEN', 'AJS_ALLOW_INSECURE')
foreach ($name in $managed) {
    Remove-Item -Path "Env:$name" -ErrorAction SilentlyContinue
}

$workDir = $null
$logDir = $null

if ($FromConfig) {
    foreach ($ignored in @('Port', 'StateDir', 'Token')) {
        if ($PSBoundParameters.ContainsKey($ignored)) {
            Write-Warn "-$ignored is ignored with -FromConfig: the config file decides the listener, the state directory and the credentials"
        }
    }
    if (-not $ConfigPath) { $ConfigPath = Join-Path $InstallDir 'ajs.env' }
    if (-not (Test-Path -LiteralPath $ConfigPath)) {
        Stop-Run "config not found: $ConfigPath (install first with install.ps1)" 2
    }
    $ConfigPath = (Resolve-Path -LiteralPath $ConfigPath).Path
    $cfg = Read-EnvFile -Path $ConfigPath
    foreach ($key in $cfg.Keys) {
        if ($key -notmatch '^AJS_[A-Z0-9_]+$') {
            Write-Warn "ignoring non-AJS key '$key' in $ConfigPath"
            continue
        }
        Set-Item -Path "Env:$key" -Value $cfg[$key]
    }
    $listen = if ($env:AJS_LISTEN) { $env:AJS_LISTEN } else { '127.0.0.1:2261' }
    $env:AJS_LISTEN = $listen
    if ($env:AJS_ALLOW_INSECURE -eq '1') {
        Write-Warn 'AJS_ALLOW_INSECURE=1 is set in the config: the admin API is unauthenticated (loopback only)'
    }
    elseif (-not $env:AJS_ADMIN_TOKEN) {
        Stop-Run "$ConfigPath sets neither AJS_ADMIN_TOKEN nor AJS_ALLOW_INSECURE=1; the daemon would refuse to start" 2
    }
    # A service runs with its working directory outside the install tree, so a
    # relative state path would land somewhere unexpected. Anchor it.
    if ($env:AJS_STATE_DIR -and -not [System.IO.Path]::IsPathRooted($env:AJS_STATE_DIR)) {
        $resolved = Join-Path $InstallDir $env:AJS_STATE_DIR
        Write-Warn "relative AJS_STATE_DIR '$($env:AJS_STATE_DIR)' resolved against ${InstallDir}: $resolved"
        $env:AJS_STATE_DIR = $resolved
    }

    $logDir = Join-Path $InstallDir 'logs'
    if (-not (Test-Path -LiteralPath $logDir)) {
        New-Item -ItemType Directory -Path $logDir -Force | Out-Null
    }
    Get-ChildItem -LiteralPath $logDir -Filter 'ajs-accountd-*.log' -ErrorAction SilentlyContinue |
        Sort-Object LastWriteTime -Descending |
        Select-Object -Skip 20 |
        Remove-Item -Force -ErrorAction SilentlyContinue
}
else {
    if ($Port -lt 0 -or $Port -gt 65535) {
        Stop-Run '-Port must be between 0 and 65535' 2
    }
    if ($Port -eq 0) { $Port = Get-FreeLoopbackPort }
    $listen = "127.0.0.1:$Port"
    $env:AJS_LISTEN = $listen
    if ($Token) {
        $env:AJS_ADMIN_TOKEN = $Token
    }
    else {
        $env:AJS_ALLOW_INSECURE = '1'
    }

    if (-not $StateDir) {
        $workDir = Join-Path ([System.IO.Path]::GetTempPath()) ('ajs-run-' + [guid]::NewGuid().ToString('N'))
        $StateDir = Join-Path $workDir 'state'
    }
    if (-not (Test-Path -LiteralPath $StateDir)) {
        New-Item -ItemType Directory -Path $StateDir -Force | Out-Null
    }
    $env:AJS_STATE_DIR = (Resolve-Path -LiteralPath $StateDir).Path
    $logDir = if ($workDir) { $workDir } else { $env:AJS_STATE_DIR }
}

$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$outLog = Join-Path $logDir "ajs-accountd-$stamp.out.log"
$errLog = Join-Path $logDir "ajs-accountd-$stamp.err.log"
$healthUrl = Get-HealthUrl -Listen $listen
$stateLabel = if ($env:AJS_STATE_DIR) { $env:AJS_STATE_DIR } else { '(daemon default ./data)' }

Write-Step "starting $Exe on $listen (state $stateLabel)"
$startArgs = @{
    FilePath     = $Exe
    ArgumentList = @('-listen', $listen)
    PassThru     = $true
    NoNewWindow  = $true
}
if (-not $StreamLog) {
    $startArgs.RedirectStandardOutput = $outLog
    $startArgs.RedirectStandardError = $errLog
}
$proc = Start-Process @startArgs

$health = Wait-Health -Url $healthUrl -TimeoutSec $TimeoutSec -Process $proc

if (-not $health) {
    if (-not $proc.HasExited) {
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
        $proc.WaitForExit(10000) | Out-Null
    }
    Write-Warn "no /healthz answer on $healthUrl within ${TimeoutSec}s"
    if (-not $StreamLog) {
        Show-LogTail -Path $outLog
        Show-LogTail -Path $errLog
    }
    if ($workDir -and -not $KeepState) {
        Remove-Item -LiteralPath $workDir -Recurse -Force -ErrorAction SilentlyContinue
    }
    Stop-Run 'the daemon did not become healthy' 1
}

Write-Host "    healthz: $($health | ConvertTo-Json -Compress)"
Write-Host "    pid:     $($proc.Id)"
if (-not $StreamLog) { Write-Host "    log:     $outLog" }

if ($Smoke) {
    if (-not $proc.HasExited) {
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
        $proc.WaitForExit(10000) | Out-Null
    }
    Write-Host '    stopped: yes'
    if ($workDir -and -not $KeepState) {
        Remove-Item -LiteralPath $workDir -Recurse -Force -ErrorAction SilentlyContinue
    }
    exit 0
}

Write-Host 'running; Ctrl+C to stop.'
$proc.WaitForExit()
exit $proc.ExitCode
