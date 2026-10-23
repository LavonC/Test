Write-Host "Building UniCompute Objective 1 binaries..." -ForegroundColor Cyan

New-Item -ItemType Directory -Force -Path "bin" | Out-Null

go build -o bin/unicompute-node.exe ./cmd/unicompute-node
go build -o bin/unicompute-coordinator.exe ./cmd/unicompute-coordinator
go build -o bin/unicompute-relay.exe ./cmd/unicompute-relay

Write-Host "Signing binaries for Windows Smart App Control / Code Integrity..." -ForegroundColor Yellow
$cert = Get-ChildItem Cert:\CurrentUser\My -CodeSigningCert | Select-Object -First 1
if (-not $cert) {
    $cert = New-SelfSignedCertificate -Type CodeSigningCert -Subject "CN=UniCompute Dev" -CertStoreLocation "Cert:\CurrentUser\My"
}
Get-ChildItem bin\*.exe | ForEach-Object {
    Set-AuthenticodeSignature -Certificate $cert -FilePath $_.FullName | Out-Null
}

Write-Host "`n[SUCCESS] Binaries built and signed successfully in .\bin\" -ForegroundColor Green
