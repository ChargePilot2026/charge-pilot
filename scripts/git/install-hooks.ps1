$ErrorActionPreference = 'Stop'
git -C "$PSScriptRoot/../.." config core.hooksPath .githooks
if ($LASTEXITCODE -ne 0) { throw 'Unable to install Git hooks' }
Write-Host 'pre-commit enabled: make lint test (requires make and Go)'
