<#
Runs the Go server in a restart loop and saves stdout/stderr to timestamped log files.
Usage:
  powershell -ExecutionPolicy Bypass -File .\scripts\run_server_watch.ps1
Optionally pass a project directory:
  powershell -ExecutionPolicy Bypass -File .\scripts\run_server_watch.ps1 -ProjectDir 'C:\path\to\project'
#>

param(
  # Default to the repo root (parent of the scripts folder)
  [string]$ProjectDir = (Split-Path -Parent $PSScriptRoot)
)

if (-not $ProjectDir -or $ProjectDir -eq '') {
  $ProjectDir = Get-Location
}

Write-Host "Server watch starting in project: $ProjectDir"

while ($true) {
  $ts = (Get-Date).ToString('yyyyMMdd_HHmmss')
  $log = Join-Path $ProjectDir "server_log_$ts.txt"
  Write-Host "Starting server; logging to $log"
  try {
    Push-Location $ProjectDir
    & go run ./cmd/server 2>&1 | Tee-Object -FilePath $log
    Pop-Location
  } catch {
    Write-Host "Error running server: $_"
  }
  Write-Host "Server exited with code $LASTEXITCODE at $(Get-Date). Restarting in 2s..."
  Start-Sleep -Seconds 2
}
