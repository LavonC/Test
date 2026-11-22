param(
    [string]$Id = "",
    [string]$Signaling = "wss://unicompute-coordinator.onrender.com/ws",
    [int]$Port = 23080,
    [switch]$Headless
)

$PSScriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path

Write-Host "=======================================================================" -ForegroundColor Cyan
Write-Host "  UniCompute Desktop Control Room (Objectives 1, 2, 3)" -ForegroundColor Cyan
Write-Host "=======================================================================" -ForegroundColor Cyan

# Locate executable
$guiExe = ""
if (Test-Path "$PSScriptRoot\unicompute-gui.exe") {
    $guiExe = "$PSScriptRoot\unicompute-gui.exe"
} elseif (Test-Path "$PSScriptRoot\bin\unicompute-gui.exe") {
    $guiExe = "$PSScriptRoot\bin\unicompute-gui.exe"
}

if (-not $guiExe) {
    if (Get-Command go -ErrorAction SilentlyContinue) {
        Write-Host "unicompute-gui.exe not found. Compiling GUI binary..." -ForegroundColor Yellow
        New-Item -ItemType Directory -Force -Path "$PSScriptRoot\bin" | Out-Null
        $env:CGO_ENABLED = "0"
        & go build -o "$PSScriptRoot\bin\unicompute-gui.exe" "$PSScriptRoot\cmd\unicompute-gui"
        if (Test-Path "$PSScriptRoot\bin\unicompute-gui.exe") {
            $guiExe = "$PSScriptRoot\bin\unicompute-gui.exe"
        }
    }
}

if (-not $guiExe) {
    Write-Host "[ERROR] Could not find unicompute-gui.exe in root or bin/" -ForegroundColor Red
    exit 1
}

# Prepare arguments
$guiArgs = @("-port", "$Port")
if ($Id) { $guiArgs += @("-id", $Id) }
if ($Signaling) { $guiArgs += @("-signaling", $Signaling) }
if ($Headless) { $guiArgs += @("-headless") }

Write-Host "Starting unicompute-gui directly on port $Port..." -ForegroundColor Green
& $guiExe @guiArgs
