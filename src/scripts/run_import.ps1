# gkill_autolog の取り込み
#
# 生ログを整理してこの端末の gkill へ直接書き込む（autolog import）。
# 同期スクリプト から呼ぶほか、手で実行してもよい。
#
# Claude は使わない。URLog の絞り込みは $AUTOLOG_HOME\url_denylist.txt で行う。
# スクリーンショットの運び出しは 同期スクリプト の dvnf move が行う。ここでは扱わない。
#
# 書き込めなかった分は台帳へ記録されないので、次回の取り込みで再処理される（要件 §17）。

[CmdletBinding()]
param(
    # 設定ファイル。ここに書いた内容を環境変数として渡す。
    [string]$EnvFile,
    # 実際には書き込まず、書き込む予定の内容だけを表示する。
    [switch]$DryRun,
    # 処理する上限時刻 (RFC3339)。既定は直近の午前4時。取りこぼしの追い込みに使う。
    [string]$Cutoff,
    # 上限を「直近まで」にする。午前4時を待たずに回すとき用。
    #
    # 既定の上限は直近の午前4時。もともと4時の定期実行を前提にした値なので、
    # 任意の時刻に回すとその日に集めた分がまるごと対象外になる。
    #
    # ただし「いま」ちょうどまでにはしない。接続系 (Wi-Fi・Bluetooth・充電) は
    # 短い切断を結合するが、結合は「次の接続」を見て初めて判定される。
    # 切断直後に処理すると本来つながる区間が2つに割れて確定し、
    # 書き込むと台帳に載るのであとから結合し直されない。
    # そのため $CutoffLagMinutes だけ手前を上限にする。
    [switch]$UntilNow
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '_gkill_api.ps1')

# -UntilNow のときに「いま」から何分手前を上限にするか。
# 接続系のマージ窓 (最長1分) を確実に超える値にする。詳細は param の説明。
# termux-tasker/autolog.sh の cutoff も同じ考え方で 2 分にしてある。
$CutoffLagMinutes = 2

# param の既定値では解決しない。Windows PowerShell 5.1 は -File で起動されたとき
# param ブロック内の $PSScriptRoot が空になるため（タスクスケジューラがこの起動方法）。
if (-not $EnvFile) { $EnvFile = Join-Path $PSScriptRoot 'autolog.env' }

if ($UntilNow -and $Cutoff) { throw '-Cutoff と -UntilNow は同時に指定できません' }

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

$logFile = Join-Path $logDir ("import_{0}.log" -f (Get-Date -Format 'yyyyMMdd'))

function Write-Log([string]$message) {
    $line = "{0} {1}" -f (Get-Date -Format 'yyyy-MM-ddTHH:mm:ssK'), $message
    Write-Host $line
    Add-Content -Path $logFile -Value $line -Encoding utf8
}

# autolog.exe を探す。
#
# npm run build の出力先を第一候補にする。
# 昔はリポジトリ直下へ置いていたので、そちらと PATH も見る。
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
    Write-Log '  探した場所: $env:AUTOLOG_EXE / release/windows_amd64/ / リポジトリ直下 / PATH'
    exit 1
}
Write-Log "autolog: $autolog"

# 書き込み先が決まっているか先に確かめる。
# 未設定のまま走らせると、全件が失敗して原因が分かりにくい。
if (-not $DryRun -and -not $env:GKILL_AUTO_PASSWORD_SHA256) {
    Write-Log 'ERROR GKILL_AUTO_PASSWORD_SHA256 が未設定。.\src\scripts\set_auto_password.ps1 を実行すること'
    exit 1
}

Write-Log "=== 取り込み開始 (dry-run=$DryRun) ==="

$importArgs = @('import')
if ($DryRun) { $importArgs += '--dry-run' }
if ($UntilNow) {
    # 最長のマージ窓 (Bluetooth とウィンドウの1分) を確実に超える値。
    # 直近 $CutoffLagMinutes 分は次回にまわるだけで失われない。
    $cutoffAt = (Get-Date).AddMinutes(-$CutoffLagMinutes).ToString('yyyy-MM-ddTHH:mm:sszzz')
    Write-Log "上限: $cutoffAt"
    $importArgs += @('--cutoff', $cutoffAt)
}
if ($Cutoff) { $importArgs += @('--cutoff', $Cutoff) }

$output = $null
$exitCode = 0

# autolog の出力を取り込む前に2つ手当てをする。どちらも 5.1 だけで起きる。
#
#   1. 出力は UTF-8。既定のコードページのままだと日本語が化ける。
#   2. $ErrorActionPreference が Stop のまま native コマンドを 2>&1 すると、
#      stderr の各行が終了エラー扱いになる。autolog は進捗ログを stderr へ出すので、
#      最初の INFO 行だけで「失敗」になってしまう。
$previousOutputEncoding = [Console]::OutputEncoding
$previousErrorAction = $ErrorActionPreference
try {
    [Console]::OutputEncoding = [Text.Encoding]::UTF8
    $ErrorActionPreference = 'Continue'

    $output = & $autolog @importArgs 2>&1
    $exitCode = $LASTEXITCODE
} finally {
    $ErrorActionPreference = $previousErrorAction
    [Console]::OutputEncoding = $previousOutputEncoding
}

$output | ForEach-Object { Write-Log "  $_" }

# 成否は終了コードだけで判断する。stderr に何か出ていても失敗とは限らない。
if ($exitCode -ne 0) {
    Write-Log "ERROR import が終了コード $exitCode で失敗"
    Write-Log '=== 取り込み終了 (失敗。次回の取り込みで再処理される) ==='
    exit 1
}

Write-Log '=== 取り込み終了 ==='
