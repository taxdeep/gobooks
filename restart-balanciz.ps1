#Requires -Version 5.1
<#
.SYNOPSIS
  One-click restart: stop whatever is listening on the Balanciz HTTP port, run templ generate, then start go run ./cmd/balanciz.

.DESCRIPTION
  - Reads APP_ADDR from .env in this folder (e.g. :6768) when -Port is omitted; default port is 6768.
  - Stops the process bound to that port (Get-NetTCPConnection, with a netstat fallback).
  - Runs templ generate unless -SkipTempl is set.
  - Starts the app in a new PowerShell window so logs stay visible (use -NoNewWindow to block this shell).

.EXAMPLE
  .\restart-balanciz.ps1

.EXAMPLE
  .\restart-balanciz.ps1 -Port 8080 -SkipTempl
#>
param(
    [int]$Port = 0,
    [switch]$SkipTempl,
    [switch]$NoNewWindow
)

$ErrorActionPreference = 'Stop'
$Root = $PSScriptRoot
Set-Location -LiteralPath $Root

function Read-PortFromEnv {
    param([string]$EnvPath)
    if (-not (Test-Path -LiteralPath $EnvPath)) { return 0 }
    $line = Get-Content -LiteralPath $EnvPath -ErrorAction SilentlyContinue |
        Where-Object { $_ -match '^\s*APP_ADDR\s*=' } |
        Select-Object -First 1
    if ($line -match ':(\d+)\s*(?:#.*)?$') { return [int]$Matches[1] }
    return 0
}

if ($Port -le 0) {
    $Port = Read-PortFromEnv (Join-Path $Root '.env')
    if ($Port -le 0) { $Port = 6768 }
}

function Stop-ListenerOnPort {
    param([int]$LocalPort)
    $seen = @{}
    $pids = @()

    try {
        $pids = @(Get-NetTCPConnection -LocalPort $LocalPort -State Listen -ErrorAction SilentlyContinue |
            Select-Object -ExpandProperty OwningProcess -Unique)
    } catch {}

    foreach ($p in $pids) {
        if ($p -gt 0 -and -not $seen.ContainsKey($p)) {
            $seen[$p] = $true
            Write-Host "Stopping PID $p (listening on port $LocalPort)..."
            Stop-Process -Id $p -Force -ErrorAction SilentlyContinue
        }
    }

    if ($seen.Count -eq 0) {
        netstat -ano 2>$null | ForEach-Object {
            if ($_ -match "LISTENING\s+(\d+)\s*$" -and $_ -match ":$LocalPort\s") {
                $procId = [int]$Matches[1]
                if ($procId -gt 0 -and -not $seen.ContainsKey($procId)) {
                    $seen[$procId] = $true
                    Write-Host "Stopping PID $procId (netstat, port $LocalPort)..."
                    Stop-Process -Id $procId -Force -ErrorAction SilentlyContinue
                }
            }
        }
    }

    if ($seen.Count -gt 0) {
        Start-Sleep -Seconds 1
    } else {
        Write-Host "No listener found on port $LocalPort (nothing to stop)."
    }
}

Write-Host "Balanciz restart - project: $Root"
Write-Host "Using port: $Port (from parameter, .env APP_ADDR, or default 6768)"
Stop-ListenerOnPort -LocalPort $Port

if (-not $SkipTempl) {
    Write-Host "Running templ generate..."
    & go run "github.com/a-h/templ/cmd/templ@v0.3.1001" generate
    if ($LASTEXITCODE -ne 0) {
        Write-Error "templ generate failed with exit code $LASTEXITCODE"
        exit $LASTEXITCODE
    }
} else {
    Write-Host "Skipping templ generate (-SkipTempl)."
}

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Error "go is not on PATH. Install Go or open a Developer shell."
    exit 1
}

if ($NoNewWindow) {
    Write-Host 'Starting go run ./cmd/balanciz (foreground, press Ctrl+C to stop)...'
    & go run ./cmd/balanciz
    exit $LASTEXITCODE
}

Write-Host "Starting go run ./cmd/balanciz in a new window..."
$childCmd = "Write-Host 'Balanciz http://127.0.0.1:$Port/'; go run ./cmd/balanciz"
Start-Process -FilePath "powershell.exe" -WorkingDirectory $Root -ArgumentList @('-NoExit', '-NoProfile', '-Command', $childCmd) | Out-Null
Write-Host "Done. Check the new PowerShell window for logs. URL: http://127.0.0.1:$($Port)/"
