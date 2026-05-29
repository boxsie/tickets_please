<#
.SYNOPSIS
    Install tickets_please as a per-user Scheduled Task running the HTTP MCP
    server on 127.0.0.1:8765. Idempotent. Pass -Uninstall to remove.

.DESCRIPTION
    The Windows counterpart to install.sh. A systemd --user service (with
    lingering) maps to a Scheduled Task triggered at logon, restarted on
    failure, and launched via `conhost --headless` so no console window
    appears. No admin rights required — everything is per-user.

    The data dir for each project is wherever you register it via
    register_agent { project_path: ... }; user-scoped agents + config live
    under %USERPROFILE%\.tickets_please.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\install.ps1

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\install.ps1 -Uninstall
#>
[CmdletBinding()]
param(
    [switch]$Uninstall,
    [string]$Addr = '127.0.0.1:8765'
)

$ErrorActionPreference = 'Stop'

$RepoDir   = $PSScriptRoot
$TaskName  = 'tickets_please'
$BinDir    = Join-Path $env:LOCALAPPDATA 'tickets_please'
$BinDst    = Join-Path $BinDir 'tickets_please.exe'
$CfgDir    = Join-Path $env:USERPROFILE '.tickets_please'
$HealthUrl = "http://$Addr/healthz"
$BaseUrl   = "http://$Addr"

function Write-Step($msg) { Write-Host "`n$msg" -ForegroundColor Yellow }
function Write-Info($msg) { Write-Host $msg -ForegroundColor Gray }
function Write-Ok($msg)   { Write-Host $msg -ForegroundColor Green }

function Require-Cmd($name) {
    if (-not (Get-Command $name -ErrorAction SilentlyContinue)) {
        Write-Host "missing required command: $name" -ForegroundColor Red
        exit 1
    }
}

function Uninstall-TicketsPlease {
    Write-Host "=== tickets_please uninstall ===" -ForegroundColor Cyan
    $existing = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    if ($existing) {
        Write-Step "Stopping + removing scheduled task..."
        Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
        Write-Info "Removed scheduled task '$TaskName'"
    }
    if (Test-Path $BinDst) {
        Remove-Item $BinDst -Force
        Write-Info "Removed $BinDst"
    }
    Write-Ok "Uninstall complete."
    Write-Info "Data left in place: $CfgDir (delete manually for a clean slate)."
}

if ($Uninstall) { Uninstall-TicketsPlease; exit 0 }

Write-Host "=== tickets_please install ===" -ForegroundColor Cyan
Require-Cmd go

# [1/6] Build the binary.
Write-Step "[1/6] Building binary..."
Push-Location $RepoDir
try {
    & go build -o (Join-Path $RepoDir 'tickets_please.exe') ./cmd/tickets_please
    if ($LASTEXITCODE -ne 0) { throw "go build failed (exit $LASTEXITCODE)" }
} finally {
    Pop-Location
}

# [2/6] Install the binary.
Write-Step "[2/6] Installing binary to $BinDst..."
New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
Copy-Item (Join-Path $RepoDir 'tickets_please.exe') $BinDst -Force
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';') -notcontains $BinDir) {
    Write-Info "note: $BinDir is not on your user PATH — add it if you want to run 'tickets_please' directly:"
    Write-Info "      [Environment]::SetEnvironmentVariable('Path', `"`$env:Path;$BinDir`", 'User')"
}

# [3/6] Initialise config + data dirs (mirrors `make init-config init-data`).
Write-Step "[3/6] Initialising config + data dirs..."
New-Item -ItemType Directory -Force -Path $CfgDir | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $CfgDir 'agents') | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $CfgDir '.staging') | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $RepoDir '.tickets_please\.staging') | Out-Null
$cfgFile = Join-Path $CfgDir 'config.yaml'
$cfgSample = Join-Path $RepoDir 'examples\config.yaml'
if ((-not (Test-Path $cfgFile)) -and (Test-Path $cfgSample)) {
    Copy-Item $cfgSample $cfgFile
    Write-Info "Wrote sample config to $cfgFile"
} else {
    Write-Info "Config already present at $cfgFile (left untouched)"
}

# [4/6] Register the scheduled task. conhost --headless runs the console
# binary with no visible window while staying attached, so the task can
# track it for restart-on-failure.
Write-Step "[4/6] Registering scheduled task '$TaskName'..."
$conhost = Join-Path $env:SystemRoot 'System32\conhost.exe'
$action = New-ScheduledTaskAction -Execute $conhost `
    -Argument "--headless `"$BinDst`" serve --addr $Addr"
$trigger = New-ScheduledTaskTrigger -AtLogOn
$principal = New-ScheduledTaskPrincipal -UserId ([Security.Principal.WindowsIdentity]::GetCurrent().Name) -LogonType Interactive
$settings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
    -StartWhenAvailable `
    -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
    -ExecutionTimeLimit ([TimeSpan]::Zero) `
    -Hidden
Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
    -Principal $principal -Settings $settings -Force | Out-Null

# [5/6] Start it now.
Write-Step "[5/6] Starting service..."
Start-ScheduledTask -TaskName $TaskName

# [6/6] Health check.
Write-Step "[6/6] Waiting for $HealthUrl"
$ok = $false
for ($i = 0; $i -lt 15; $i++) {
    try {
        Invoke-WebRequest -UseBasicParsing -TimeoutSec 1 -Uri $HealthUrl | Out-Null
        $ok = $true; break
    } catch {
        Start-Sleep -Seconds 1
        Write-Host '.' -NoNewline
    }
}
Write-Host ''

if (-not $ok) {
    Write-Host "Service did not respond within 15s." -ForegroundColor Red
    Write-Info "Inspect the task: Get-ScheduledTaskInfo -TaskName $TaskName"
    Write-Info "Run the server in the foreground to see logs: & `"$BinDst`" serve --addr $Addr"
    exit 1
}

Write-Host "`n=== Install complete ===" -ForegroundColor Green
Write-Host "Service:  " -ForegroundColor Cyan -NoNewline; Write-Host "Get-ScheduledTaskInfo -TaskName $TaskName"
Write-Host "Stop:     " -ForegroundColor Cyan -NoNewline; Write-Host "Stop-ScheduledTask -TaskName $TaskName"
Write-Host "Web UI:   " -ForegroundColor Cyan -NoNewline; Write-Host "$BaseUrl/"
Write-Host "Wire MCP: " -ForegroundColor Cyan -NoNewline; Write-Host "claude mcp add --transport http tickets_please $BaseUrl/mcp"
