[CmdletBinding()]
param(
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$')]
    [string]$Version = (Get-Content -LiteralPath (Join-Path $PSScriptRoot '../VERSION') -Raw).Trim()
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$outputRoot = Join-Path $projectRoot "release/$Version"
$utf8 = New-Object System.Text.UTF8Encoding($false)
$environmentNames = @('VITE_REACT_APP_VERSION', 'CGO_ENABLED', 'GOOS', 'GOARCH')
$previousEnvironment = @{}
foreach ($name in $environmentNames) {
    $previousEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}

Push-Location $projectRoot
try {
    foreach ($command in @('go', 'bun', 'tar')) {
        if (-not (Get-Command $command -ErrorAction SilentlyContinue)) {
            throw "Required command is not available: $command"
        }
    }
    $hasGitMetadata = Test-Path -LiteralPath (Join-Path $projectRoot '.git')
    $sourceRevision = 'source-tree'
    $buildVcs = 'false'
    if ($hasGitMetadata) {
        if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
            throw 'Git is required when building from a Git checkout.'
        }
        $sourceRevision = (& git rev-parse HEAD).Trim()
        if ($LASTEXITCODE -ne 0) { throw 'Cannot read the source revision.' }
        $sourceChanges = & git status --porcelain
        if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect Git status.' }
        if ($sourceChanges) { throw 'Commit source changes before building a release.' }
        $buildVcs = 'true'
    }
    if (Test-Path -LiteralPath $outputRoot) {
        throw "Release output already exists: $outputRoot. Use a different version."
    }
    $windowsDirectory = Join-Path $outputRoot 'windows-amd64'
    $linuxDirectory = Join-Path $outputRoot 'linux-amd64'
    New-Item -ItemType Directory -Path $windowsDirectory, $linuxDirectory -Force | Out-Null

    $goVersion = (& go version).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'Cannot read the Go version.' }
    $bunVersion = (& bun --version).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'Cannot read the Bun version.' }
    $env:VITE_REACT_APP_VERSION = $Version
    Push-Location (Join-Path $projectRoot 'web')
    try {
        & bun install --frozen-lockfile
        if ($LASTEXITCODE -ne 0) { throw 'Frontend dependency installation failed.' }
        & bun run build
        if ($LASTEXITCODE -ne 0) { throw 'Frontend production build failed.' }
    } finally {
        Pop-Location
    }
    if ($hasGitMetadata) {
        $sourceChanges = & git status --porcelain
        if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect Git status after building.' }
        if ($sourceChanges) {
            throw 'The frontend build changed source files; inspect them before releasing.'
        }
    }

    $env:CGO_ENABLED = '0'
    $env:GOARCH = 'amd64'
    $linkerFlags = "-s -w -X github.com/QuantumNous/new-api/common.Version=$Version"
    $windowsExecutable = Join-Path $windowsDirectory 'new-api.exe'
    $linuxExecutable = Join-Path $linuxDirectory 'new-api'
    $env:GOOS = 'windows'
    & go build -trimpath "-buildvcs=$buildVcs" -ldflags $linkerFlags -o $windowsExecutable .
    if ($LASTEXITCODE -ne 0) { throw 'Windows build failed.' }
    $env:GOOS = 'linux'
    & go build -trimpath "-buildvcs=$buildVcs" -ldflags $linkerFlags -o $linuxExecutable .
    if ($LASTEXITCODE -ne 0) { throw 'Static Linux build failed.' }

    $reportedVersion = (& $windowsExecutable --version).Trim()
    if ($LASTEXITCODE -ne 0 -or $reportedVersion -ne $Version) {
        throw 'The Windows release did not report the expected version.'
    }
    $instructions = @"
New API - classroom build
Version: $Version
Source revision: $sourceRevision
Upstream: QuantumNous/new-api

Windows:
  Extract the ZIP into a permanent folder and double-click start.cmd.
  Or run: .\new-api.exe --port 3000

Linux x86-64:
  Extract the tar.gz into a permanent folder, then run:
  chmod +x new-api start.sh
  ./start.sh
  This is a static CGO_ENABLED=0 executable without a glibc dependency.

Open http://localhost:3000 on the host and create an administrator account.
Student API base URL: http://HOST-LAN-IP:3000/v1
Use a teaching API key, not the upstream provider key.
Direct connections need no TRUSTED_PROXIES setting (the default is none).
Only set TRUSTED_PROXIES when using a reverse proxy; list the proxy's addresses.
No Go, Bun, Docker, Redis, or separate database installation is needed to run.
The web console is embedded. SQLite is used unless SQL_DSN is configured.

Classroom controls: System settings > Request policies
  Response reuse: enable response reuse, SSE reuse and concurrent sharing.
  Start with full-request matching. Prefix mode is only for fixed answers.
  IP and API key limits: enable and save rules before creating classrooms.
  Classroom billing: choose a key, IP ranges and independent limit rules.
  Different active classes on one key require disjoint gateway-visible IPs.
  Resetting class limits preserves bills, other classes and shared budgets.
  Export CSV from View bill. Amounts use configured gateway pricing;
  reconcile with provider payment receipts for reimbursement.

Persistence and upgrades:
  Always start in the same folder. start.cmd/start.sh do this automatically.
  Stop the process before backing up the entire runtime folder.
  Preserve the SQLite database, .relay-guard folder and any .env configuration.
  When updating, replace the executable and start script, retaining runtime data.
  .env.example lists optional configuration; do not overwrite an existing .env.

See LICENSE, NOTICE and THIRD-PARTY-LICENSES.md for attribution and terms.
"@
    $windowsLauncher = "@echo off`r`ncd /d `"%~dp0`"`r`nnew-api.exe --port 3000`r`npause`r`n"
    $linuxLauncher = @'
#!/bin/sh
set -eu
cd -- "$(dirname -- "$0")"
exec ./new-api --port "${PORT:-3000}"
'@
    [IO.File]::WriteAllText((Join-Path $windowsDirectory 'start.cmd'), $windowsLauncher, $utf8)
    [IO.File]::WriteAllText((Join-Path $linuxDirectory 'start.sh'), $linuxLauncher.Replace("`r`n", "`n") + "`n", $utf8)
    foreach ($directory in @($windowsDirectory, $linuxDirectory)) {
        foreach ($file in @('LICENSE', 'NOTICE', 'THIRD-PARTY-LICENSES.md', '.env.example')) {
            Copy-Item -LiteralPath (Join-Path $projectRoot $file) -Destination $directory
        }
        [IO.File]::WriteAllText((Join-Path $directory 'README.txt'), $instructions, $utf8)
    }

    $windowsArchive = "new-api-$Version-windows-amd64.zip"
    $linuxArchive = "new-api-$Version-linux-amd64-static.tar.gz"
    Compress-Archive -Path (Join-Path $windowsDirectory '*') -DestinationPath (Join-Path $outputRoot $windowsArchive) -CompressionLevel Optimal
    & tar -czf (Join-Path $outputRoot $linuxArchive) -C $linuxDirectory .
    if ($LASTEXITCODE -ne 0) { throw 'Linux archive creation failed.' }
    $artifactNames = @($windowsArchive, $linuxArchive, 'windows-amd64/new-api.exe', 'linux-amd64/new-api')
    $checksums = foreach ($artifact in $artifactNames) {
        $hash = (Get-FileHash -LiteralPath (Join-Path $outputRoot $artifact) -Algorithm SHA256).Hash.ToLowerInvariant()
        "$hash  $artifact"
    }
    [IO.File]::WriteAllText((Join-Path $outputRoot 'SHA256SUMS.txt'), ($checksums -join "`n") + "`n", $utf8)
    $metadata = [ordered]@{
        version = $Version
        source_revision = $sourceRevision
        built_at_utc = [DateTime]::UtcNow.ToString('o')
        go = $goVersion
        bun = $bunVersion
        cgo_enabled = 0
        targets = @('windows/amd64', 'linux/amd64 (static)')
        artifacts = $artifactNames
    }
    [IO.File]::WriteAllText((Join-Path $outputRoot 'BUILD-INFO.json'), ($metadata | ConvertTo-Json -Depth 4) + "`n", $utf8)
    Write-Host "Release ready: $outputRoot"
} finally {
    foreach ($name in $environmentNames) {
        [Environment]::SetEnvironmentVariable($name, $previousEnvironment[$name], 'Process')
    }
    Pop-Location
}
