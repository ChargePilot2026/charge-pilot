param(
    [ValidateSet(1,2)][int]$Number = 1,
    [ValidateRange(1,20)][int]$Ports = 2,
    [string]$DeviceID = ''
)
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path "$PSScriptRoot/..").Path
if (-not $DeviceID) { $DeviceID = if ($Number -eq 1) { '5348240514082652' } else { '5348240514082653' } }
if ($DeviceID -notmatch '^\d{16}$') { throw 'DC589 device ID must be exactly 16 decimal digits.' }
$temporary = Join-Path $root '.tmp'
New-Item -ItemType Directory -Path $temporary -Force | Out-Null
$binary = Join-Path $temporary "simulator-dc589-$Number.exe"
Push-Location $root
try {
    go build -o $binary ./cmd/simulator-dc589
    if ($LASTEXITCODE -ne 0) { throw 'Simulator build failed. Go 1.27 is required.' }
    $control = "127.0.0.1:$((9189 + $Number))"
    $state = Join-Path $temporary "simulator-$Number-$DeviceID.json"
    & $binary -board-id $DeviceID -ports $Ports -control $control -state-file $state
    if ($LASTEXITCODE -ne 0) { throw 'Simulator stopped with an error. Check the displayed message.' }
} finally { Pop-Location }
