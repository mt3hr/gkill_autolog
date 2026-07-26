# この機械向けの autolog をビルドする。
#
# 出力はリポジトリ直下の autolog.exe。
# 収集・取り込みのスクリプトはこの場所を見る。

[CmdletBinding()]
param(
    # 出力先。既定はリポジトリ直下の autolog.exe。
    [string]$OutFile
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '_gkill_api.ps1')

if (-not $OutFile) { $OutFile = Join-Path (Get-AutologRoot $PSScriptRoot) 'autolog.exe' }

Push-Location (Get-AutologModuleDir $PSScriptRoot)
try {
    & go build -trimpath -o $OutFile ./cmd/autolog
    if ($LASTEXITCODE -ne 0) { throw 'go build に失敗しました' }
} finally {
    Pop-Location
}

$size = [math]::Round((Get-Item $OutFile).Length / 1MB, 1)
Write-Host "$OutFile ($size MB)"
