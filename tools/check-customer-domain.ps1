#!/usr/bin/env pwsh
<#
.SYNOPSIS
  docs 子域名一致性检查脚本

.DESCRIPTION
  扫描 docs/**/*.md 与 README.md,检查:
    1. 文档统一使用 `<customer-domain>` 而非 `api.<customer-domain>`(避免在客户文档中暗示子域名前缀)
    2. 检查后无残留 `api.<customer-domain>`

  触发:代码动工后首次跑一次,后续每次 docs PR 跑一次

.EXAMPLE
  pwsh tools/check-customer-domain.ps1
#>

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
$docsDir = Join-Path $root 'docs'
$readme  = Join-Path $root 'README.md'

$utf8NoBom = New-Object System.Text.UTF8Encoding $false
$findings = @()

function Check-File($file) {
  $lines = [System.IO.File]::ReadAllLines($file, [System.Text.Encoding]::UTF8)
  for ($i = 0; $i -lt $lines.Length; $i++) {
    if ($lines[$i] -match '`?api\.<customer-domain>`?') {
      $findings += [pscustomobject]@{ File = $file; Line = $i + 1; Text = $lines[$i] }
    }
  }
}

# 扫所有 docs/**/*.md
Get-ChildItem -Path $docsDir -Filter *.md -Recurse | ForEach-Object { Check-File $_.FullName }
# README.md
Check-File $readme

if ($findings.Count -eq 0) {
  Write-Output '✓ 子域名格式正确(全部使用 <customer-domain>)'
  exit 0
}

Write-Output ("X 发现 {0} 处残留 api.<customer-domain>:" -f $findings.Count)
foreach ($f in $findings) {
  $rel = $f.File.Replace($root, '')
  Write-Output ("  {0}:{1}  {2}" -f $rel, $f.Line, $f.Text.Trim())
}
exit 1