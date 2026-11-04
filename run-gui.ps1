Write-Host "=======================================================================" -ForegroundColor Cyan
Write-Host "  UniCompute Desktop Control Room (Objectives 1, 2, 3)" -ForegroundColor Cyan
Write-Host "=======================================================================" -ForegroundColor Cyan
Write-Host "Starting daemon and launching standalone desktop window..." -ForegroundColor Yellow
go run ./cmd/unicompute-gui @args
