#Requires -Version 5.1
<#
.SYNOPSIS
    Download and verify the Windows asset set of one ajs-accountd GitHub release.

.DESCRIPTION
    Downloads ajs-accountd-windows-amd64.exe, MANIFEST.txt and SHA256SUMS with
    `gh release download`, then:

      * checks every downloaded file against its SHA256SUMS entry (mandatory),
      * parses MANIFEST.txt and requires its version: line to equal the tag,
      * runs the downloaded binary with -version and compares the stamped
        version (a mismatch fails; a binary that cannot execute here is
        reported as [UNVERIFIED]),
      * requires `gh release verify-asset` and `gh attestation verify` to pass
        for the Windows binary (mandatory unless -AllowUnverified).

    The checksum chain and the manifest are never skipped. -AllowUnverified is
    the only opt-out from the attestation requirement: it downgrades a failed
    or unavailable attestation check to a warning and never skips a hash.
    There is no Authenticode signature on these assets, so this script makes
    no claim about code signing.

    Exit codes: 0 verified, 1 verification failed, 2 usage or precondition.

.PARAMETER Tag
    Release tag, for example v0.1.0-pre.2. Default: the latest release.

.PARAMETER Repo
    OWNER/REPO that owns the release. Default: dwgx/AIJIASU2API.

.PARAMETER Dir
    Download directory. Default: a fresh directory under $env:TEMP.

.PARAMETER RequireAttestation
    Accepted for explicitness: attestations are already required. Combining it
    with -AllowUnverified is refused (exit 2).

.PARAMETER AllowUnverified
    The only opt-out from the attestation requirement: downgrade a failed or
    unavailable attestation check to a warning. Never affects SHA256SUMS or
    MANIFEST.txt.

.PARAMETER Keep
    Keep the download directory. -PassThru implies -Keep.

.PARAMETER PassThru
    Emit one object describing the verified set and keep the download
    directory so the caller can install the binary.

.EXAMPLE
    ./verify-release.ps1 -Tag v0.1.0-pre.2 -RequireAttestation

.EXAMPLE
    $v = ./verify-release.ps1 -Tag v0.1.0-pre.2 -PassThru
    $v.ExePath; $v.Version; $v.Sha256
#>
[CmdletBinding()]
param(
    [string]$Tag,
    [string]$Repo = 'dwgx/AIJIASU2API',
    [string]$Dir,
    [switch]$RequireAttestation,
    [switch]$AllowUnverified,
    [switch]$Keep,
    [switch]$PassThru
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

# Set before any failure path runs: Stop-Verify removes a temp directory only
# when this script created it.
$script:createdDir = $false
$script:workDir = ''

$ExeName = 'ajs-accountd-windows-amd64.exe'
$ManifestName = 'MANIFEST.txt'
$SumsName = 'SHA256SUMS'

function Write-Step {
    param([string]$Message)
    Write-Host "==> $Message"
}

# PowerShell 5.1 turns a native command's stderr into a terminating error when
# $ErrorActionPreference is Stop, and gh writes progress and failures there.
# Capture both streams as text and decide on the exit code instead.
function Invoke-Gh {
    param([string[]]$Arguments)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $output = (& gh @Arguments 2>&1 | ForEach-Object {
                if ($_ -is [System.Management.Automation.ErrorRecord]) { $_.Exception.Message } else { "$_" }
            } | Out-String).Trim()
        $code = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previous
    }
    return [pscustomobject]@{ ExitCode = $code; Output = $output }
}

function Write-Warn {
    param([string]$Message)
    Write-Host "warning: $Message" -ForegroundColor Yellow
}

function Stop-Verify {
    param([string]$Message, [int]$Code = 1)
    Write-Host "verify-release: $Message" -ForegroundColor Red
    if ($script:createdDir -and $script:workDir -and (Test-Path -LiteralPath $script:workDir)) {
        Remove-Item -LiteralPath $script:workDir -Recurse -Force -ErrorAction SilentlyContinue
    }
    exit $Code
}

if ($RequireAttestation -and $AllowUnverified) {
    Stop-Verify '-RequireAttestation and -AllowUnverified contradict each other; pass one or the other' 2
}

if (-not (Get-Command gh -ErrorAction SilentlyContinue)) {
    Stop-Verify 'the GitHub CLI (gh) is not on PATH; install it from https://cli.github.com/ and run gh auth login' 2
}

$Tag = $Tag.Trim()
if (-not $Tag) {
    # `gh release view` without a tag skips prereleases; this project publishes
    # prereleases only, so list them newest-first instead.
    $result = Invoke-Gh @('release', 'list', '--repo', $Repo, '--limit', '1', '--exclude-drafts', '--json', 'tagName', '--jq', '.[0].tagName')
    $Tag = $result.Output
    if ($result.ExitCode -ne 0 -or -not $Tag -or $Tag -eq 'null') {
        Stop-Verify "could not resolve the latest release for $Repo; pass -Tag explicitly" 2
    }
}
if ($Tag -match '[\s"]') {
    Stop-Verify "invalid tag '$Tag'" 2
}

$createdDir = $false
if (-not $Dir) {
    $Dir = Join-Path ([System.IO.Path]::GetTempPath()) ('ajs-verify-' + [guid]::NewGuid().ToString('N'))
    $createdDir = $true
}
if (-not (Test-Path -LiteralPath $Dir)) {
    New-Item -ItemType Directory -Path $Dir -Force | Out-Null
}
$Dir = (Resolve-Path -LiteralPath $Dir).Path
$script:createdDir = $createdDir
$script:workDir = $Dir

Write-Step "downloading $Tag from $Repo into $Dir"
$ghArgs = @('release', 'download', $Tag, '--repo', $Repo, '--dir', $Dir, '--clobber')
foreach ($name in @($ExeName, $ManifestName, $SumsName)) {
    $ghArgs += @('--pattern', $name)
}
$result = Invoke-Gh $ghArgs
if ($result.ExitCode -ne 0) {
    Stop-Verify "gh release download $Tag failed (exit $($result.ExitCode)):`n$($result.Output)" 1
}
Get-ChildItem -LiteralPath $Dir -File | ForEach-Object {
    Unblock-File -LiteralPath $_.FullName -ErrorAction SilentlyContinue
}

Write-Step 'checking SHA256SUMS'
$sumsPath = Join-Path $Dir $SumsName
if (-not (Test-Path -LiteralPath $sumsPath)) {
    Stop-Verify "$SumsName was not downloaded" 1
}
$sums = @{}
foreach ($line in Get-Content -LiteralPath $sumsPath) {
    if ($line -match '^\s*([0-9A-Fa-f]{64})\s+\*?(.+?)\s*$') {
        $sums[$Matches[2]] = $Matches[1].ToLowerInvariant()
    }
}
if ($sums.Count -eq 0) {
    Stop-Verify "$SumsName contains no sha256 entries" 1
}

$hashes = @{}
$checked = 0
foreach ($name in @($ExeName, $ManifestName)) {
    $path = Join-Path $Dir $name
    if (-not (Test-Path -LiteralPath $path)) {
        Stop-Verify "release asset $name is missing from $Tag" 1
    }
    if (-not $sums.ContainsKey($name)) {
        Stop-Verify "$SumsName has no entry for $name" 1
    }
    $actual = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $sums[$name]) {
        Stop-Verify "$name sha256 mismatch: SHA256SUMS says $($sums[$name]), the downloaded file is $actual" 1
    }
    $hashes[$name] = $actual
    $checked++
}
Write-Host ("    {0}/{1} SHA256SUMS entries verified ({2} assets were not downloaded)" -f $checked, $sums.Count, ($sums.Count - $checked))

Write-Step 'parsing MANIFEST.txt'
$manifestPath = Join-Path $Dir $ManifestName
$version = $null
$commit = $null
foreach ($line in Get-Content -LiteralPath $manifestPath) {
    if (-not $version -and $line -match '^version:\s*(\S+)') {
        $version = $Matches[1]
    }
    elseif (-not $commit -and $line -match '^commit:\s*(\S+)') {
        $commit = $Matches[1]
    }
}
if (-not $version) {
    Stop-Verify 'MANIFEST.txt has no version: line' 1
}
if (-not $commit) {
    Stop-Verify 'MANIFEST.txt has no commit: line' 1
}
if ($version -ne $Tag) {
    Stop-Verify "MANIFEST.txt says version '$version' but the tag is '$Tag'" 1
}

Write-Step 'probing the downloaded binary (-version)'
$exePath = Join-Path $Dir $ExeName
$probe = ''
$probeExit = -1
try {
    $probe = (& $exePath -version 2>&1 | Out-String).Trim()
    $probeExit = $LASTEXITCODE
}
catch {
    $probeExit = -1
}
if ($probeExit -ne 0) {
    Write-Warn "the downloaded binary did not run here (-version exit $probeExit); the stamped version stays [UNVERIFIED]"
}
elseif ($probe -match '^ajs-accountd\s+(\S+)\s+\(([^)]*)\)') {
    if ($Matches[1] -ne $version) {
        Stop-Verify "the binary reports version '$($Matches[1])' but MANIFEST.txt says '$version'" 1
    }
    Write-Host "    $probe"
}
else {
    Write-Warn "unexpected -version output, identity not confirmed: $probe"
}

# Attestations are required by default, matching deploy/verify-release.sh.
# -AllowUnverified is the only opt-out; it never relaxes the checksum chain.
$attestation = 'verified'
Write-Step 'checking release attestations'
$failure = $null

$result = Invoke-Gh @('release', 'verify-asset', $Tag, $exePath, '--repo', $Repo)
if ($result.ExitCode -ne 0) {
    $failure = "gh release verify-asset failed for $ExeName (exit $($result.ExitCode)):`n$($result.Output)"
}
else {
    Write-Host "    gh release verify-asset: ok"
}

if (-not $failure) {
    $result = Invoke-Gh @('attestation', 'verify', $exePath, '--repo', $Repo)
    if ($result.ExitCode -ne 0) {
        $failure = "gh attestation verify failed for $ExeName (exit $($result.ExitCode)):`n$($result.Output)"
    }
    else {
        Write-Host "    gh attestation verify: ok"
    }
}

if ($failure) {
    if ($AllowUnverified) {
        Write-Warn $failure
        Write-Warn 'continuing because -AllowUnverified was passed; provenance is [UNVERIFIED]'
        $attestation = 'unavailable'
    }
    else {
        $hint = 'The checksum chain is intact but provenance is unproven. Re-run with -AllowUnverified to accept that (releases published before attestations existed, such as v0.1.0-pre.2, cannot pass this check).'
        Stop-Verify "$failure`n$hint" 1
    }
}

Write-Step "verified $Tag"
Write-Host "    manifest version: $version"
Write-Host "    manifest commit:  $commit"
Write-Host "    binary sha256:    $($hashes[$ExeName])"
Write-Host "    attestation:      $attestation"
Write-Host "    download dir:     $Dir"

if ($PassThru) {
    [pscustomobject]@{
        Tag            = $Tag
        Version        = $version
        Commit         = $commit
        ExeName        = $ExeName
        ExePath        = $exePath
        Sha256         = $hashes[$ExeName]
        ManifestSha256 = $hashes[$ManifestName]
        Dir            = $Dir
        Attestation    = $attestation
    }
}

if ($createdDir -and -not $Keep -and -not $PassThru) {
    Remove-Item -LiteralPath $Dir -Recurse -Force -ErrorAction SilentlyContinue
}

exit 0
