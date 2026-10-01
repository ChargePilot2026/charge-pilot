param()
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path "$PSScriptRoot/../..").Path
docker compose -f "$root/compose.dev.yaml" up -d
if ($LASTEXITCODE -ne 0) { throw 'Development stack did not start. Check Docker Desktop and compose logs.' }
Write-Host 'Admin: http://127.0.0.1:5173 (admin / ChangeMe!Admin2026)'
Write-Host 'User H5: http://127.0.0.1:5174 (development account tester-1)'
Write-Host 'First install can take a minute. Open two terminals for scripts/dev/simulator.ps1 -Number 1 and -Number 2.'
Write-Host 'Guide: docs/local-test-guide.md. This command preserves existing development data.'
