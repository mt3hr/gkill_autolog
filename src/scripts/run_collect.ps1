# gkill_autolog の常駐収集
#
# autolog.env を環境変数として読み込んでから autolog collect を起動する。
# タスクスケジューラ (register_tasks.ps1) はこれ経由で collect を起動する。
#
# collect を素で起動すると autolog.env が効かず、端末名や撮影間隔などの
# 設定が既定値のまま動いてしまう（取り込み側の run_import.ps1 と非対称だった）。
#
# collect は常駐するので、このスクリプトも起動している間は返らない。
# 出力は $AUTOLOG_HOME\logs\collect_YYYYMMDD.log へ残す。

[CmdletBinding()]
param(
    # 設定ファイル。ここに書いた内容を環境変数として渡す。
    [string]$EnvFile
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '_gkill_api.ps1')

# param の既定値では解決しない。Windows PowerShell 5.1 は -File で起動されたとき
# param ブロック内の $PSScriptRoot が空になるため（タスクスケジューラがこの起動方法）。
if (-not $EnvFile) { $EnvFile = Join-Path $PSScriptRoot 'autolog.env' }

# ---------------------------------------------------------------- 設定の読み込み

if (Test-Path $EnvFile) {
    # 必ず UTF-8 として読む。Get-Content に任せると 5.1 が Shift_JIS として読み、
    # 日本語コメントの直後の行が黙って読み落とされる。
    $settings = Read-GkillEnvFile $EnvFile
    foreach ($key in $settings.Keys) {
        Set-Item -Path "env:$key" -Value $settings[$key]
    }
} else {
    Write-Warning "設定ファイルが無い: $EnvFile"
}

$autologHome = $env:AUTOLOG_HOME
if (-not $autologHome) { $autologHome = Join-Path $env:LOCALAPPDATA 'gkill_autolog' }
$logDir = Join-Path $autologHome 'logs'
New-Item -ItemType Directory -Force -Path $logDir | Out-Null

# 常駐したまま日付をまたぐので、ログファイル名は書くたびに決める。
function Write-Log([string]$message) {
    $line = "{0} {1}" -f (Get-Date -Format 'yyyy-MM-ddTHH:mm:ssK'), $message
    Write-Host $line
    $logFile = Join-Path $logDir ("collect_{0}.log" -f (Get-Date -Format 'yyyyMMdd'))
    Add-Content -Path $logFile -Value $line -Encoding utf8
}

# autolog.exe を探す。run_import.ps1 と同じ順序。
$autolog = $env:AUTOLOG_EXE
if (-not $autolog) {
    $root = Get-AutologRoot $PSScriptRoot
    $candidates = @(
        (Join-Path $root 'release/windows_amd64/autolog.exe')
        (Join-Path $root 'autolog.exe')
    )
    $autolog = $candidates | Where-Object { Test-Path $_ } | Select-Object -First 1
    if (-not $autolog) {
        $onPath = Get-Command autolog -ErrorAction SilentlyContinue
        if ($onPath) { $autolog = $onPath.Source }
    }
}

if (-not $autolog -or -not (Test-Path $autolog)) {
    Write-Log 'ERROR autolog.exe が見つからない。npm run build を実行すること'
    exit 1
}

Write-Log "=== 常駐収集を開始 ($autolog) ==="

# autolog の出力を取り込む前に2つ手当てをする。どちらも 5.1 だけで起きる。
#
#   1. 出力は UTF-8。既定のコードページのままだと日本語が化ける。
#   2. $ErrorActionPreference が Stop のまま native コマンドを 2>&1 すると、
#      stderr の各行が終了エラー扱いになる。collect は進捗ログを stderr へ出す。
$previousOutputEncoding = [Console]::OutputEncoding
$previousErrorAction = $ErrorActionPreference
$exitCode = 0
try {
    [Console]::OutputEncoding = [Text.Encoding]::UTF8
    $ErrorActionPreference = 'Continue'

    & $autolog collect 2>&1 | ForEach-Object { Write-Log "  $_" }
    $exitCode = $LASTEXITCODE
} finally {
    $ErrorActionPreference = $previousErrorAction
    [Console]::OutputEncoding = $previousOutputEncoding
}

# 成否は終了コードだけで判断する。stderr に何か出ていても失敗とは限らない。
if ($exitCode -ne 0) {
    Write-Log "ERROR collect が終了コード $exitCode で終了"
    exit 1
}

Write-Log '=== 常駐収集を終了 ==='
