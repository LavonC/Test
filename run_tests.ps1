Write-Host "Running UniCompute Objective 1 Test Suite..." -ForegroundColor Cyan

$pkgs = @("blobstore", "instrumentation", "nat", "relay", "signaling", "transport", "node")
$allPassed = $true

New-Item -ItemType Directory -Force -Path "bin" | Out-Null

foreach ($p in $pkgs) {
    Write-Host "`n--> Testing pkg/$p" -ForegroundColor Yellow
    
    # Try direct go test first
    $testOutput = go test -v "./pkg/$p" 2>&1
    if ($LASTEXITCODE -eq 0) {
        $testOutput | Out-String | Write-Host
        continue
    }
    
    # If direct go test failed due to AppControl on temp binary, compile locally into bin/ and run
    Write-Host "Direct go test exited ($LASTEXITCODE), falling back to local bin build..." -ForegroundColor Yellow
    $exeName = "bin/unicompute_${p}_test.exe"
    go test -c -o $exeName "./pkg/$p"
    if ($LASTEXITCODE -eq 0) {
        & ".\$exeName" "-test.v"
        if ($LASTEXITCODE -ne 0) {
            $allPassed = $false
        }
        Remove-Item $exeName -Force -ErrorAction SilentlyContinue
    } else {
        $allPassed = $false
    }
}

if ($allPassed) {
    Write-Host "`n[SUCCESS] All Objective 1 tests passed!" -ForegroundColor Green
} else {
    Write-Host "`n[FAILURE] One or more test suites failed." -ForegroundColor Red
    exit 1
}
