# Android (arm64) 向けの autolog をビルドして Dropbox へ置く。
#
# Termux の gkill_server と同じ配り方にしている。
# 端末側は update_autolog.sh が dropbox:/autolog を go/bin へ落として使う。
#
# CGO は使わない。modernc.org/sqlite が pure Go なので不要で、
# Windows 上で NDK の clang を指定すると Windows バイナリが出てしまう
# （feedback: Android クロスコンパイルは CGO_ENABLED=0）。

[CmdletBinding()]
param(
    # 出力先。既定はリポジトリ直下の release/。
    [string]$OutDir,
    # ビルドだけして Dropbox へは上げない。
    [switch]$SkipUpload
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '_gkill_api.ps1')

if (-not $OutDir) { $OutDir = Join-Path (Get-AutologRoot $PSScriptRoot) 'release' }
if (-not (Test-Path $OutDir)) { New-Item -ItemType Directory -Path $OutDir | Out-Null }
$outFile = Join-Path $OutDir 'autolog'

Write-Host '=== ビルド ==='
Push-Location (Get-AutologModuleDir $PSScriptRoot)
try {
    $env:CGO_ENABLED = '0'
    $env:GOOS = 'android'
    $env:GOARCH = 'arm64'
    & go build -trimpath -ldflags '-s -w' -o $outFile ./cmd/autolog
    if ($LASTEXITCODE -ne 0) { throw 'go build に失敗しました' }
} finally {
    Pop-Location
    Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
}

# 出力が本当に ARM64 の ELF か確かめる。
# Windows でクロスコンパイルすると、設定を1つ間違えるだけで
# 中身が Windows バイナリ (MZ) のまま出来上がることがある。
$head = [System.IO.File]::ReadAllBytes($outFile)[0..0x12]
$isElf = $head[0] -eq 0x7F -and $head[1] -eq 0x45 -and $head[2] -eq 0x4C -and $head[3] -eq 0x46
$isArm64 = $head[0x12] -eq 0xB7
if (-not $isElf) { throw "ELF ではありません。Windows バイナリになっている可能性があります: $outFile" }
if (-not $isArm64) { throw "ARM64 ではありません: $outFile" }

$size = [math]::Round((Get-Item $outFile).Length / 1MB, 1)
Write-Host "  $outFile ($size MB, ELF ARM64)"

if ($SkipUpload) {
    Write-Host ''
    Write-Host '(-SkipUpload のため Dropbox へは上げません)'
    Write-Host '端末へ手で入れる場合:'
    Write-Host "  adb push $outFile /sdcard/autolog"
    Write-Host '  (Termux で) cp /sdcard/autolog ~/go/bin/ && chmod +x ~/go/bin/autolog'
    return
}

Write-Host ''
Write-Host '=== Dropbox へ配置 ==='
if (-not (Get-Command hbg -ErrorAction SilentlyContinue)) {
    throw 'hbg が PATH にありません。-SkipUpload を付けるか hbg を用意してください'
}
# hbg copy に上書きの指定は無い。同名なら更新される（update_gkill.sh と同じ使い方）。
& hbg copy "local:$outFile" 'dropbox:/'
if ($LASTEXITCODE -ne 0) { throw 'hbg copy に失敗しました' }

Write-Host '  dropbox:/autolog に置きました'
Write-Host ''
Write-Host '端末側での取り込み:'
Write-Host '  ~/.termux/tasker/update_autolog.sh'
Write-Host '  ~/.termux/tasker/autolog.sh'
