# 端末別ユーザー（<接頭辞><端末名>）共通のパスワードを設定する。
#
# autolog import はこのパスワードで各ユーザーへログインし、
# gkill の HTTP API を直接叩いて書き込む（MCP は経由しない）。
#
# setup_auto_users.ps1 で設定したものと同じ値を入れること。
# パスワードは画面に出さず、ファイルにも平文では残さない。
# Windows PowerShell 5.1 と PowerShell 7 の両方で動く。

[CmdletBinding()]
param(
    [string]$EnvFile
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '_gkill_api.ps1')

# param の既定値では解決しない。Windows PowerShell 5.1 は -File で起動されたとき
# param ブロック内の $PSScriptRoot が空になるため（タスクスケジューラがこの起動方法）。
if (-not $EnvFile) { $EnvFile = Join-Path $PSScriptRoot 'autolog.env' }

# 設定ファイルは必ず UTF-8 として読む。
# Get-Content に任せると 5.1 が Shift_JIS として読み、
# 日本語コメントの直後の行が黙って読み落とされる。
$settings = Read-GkillEnvFile $EnvFile

$prefix = $settings['GKILL_AUTO_USER_PREFIX']
if (-not $prefix) { throw "GKILL_AUTO_USER_PREFIX が $EnvFile にありません" }

$base = $settings['GKILL_BASE_URL']
if (-not $base) { throw "GKILL_BASE_URL が $EnvFile にありません" }
if ($settings['GKILL_INSECURE'] -in @('true', '1', 'yes', 'on')) { Enable-GkillInsecureTls }

# AUTOLOG_ALLOWED_DEVICES は省略できる（autolog.env.example・Go 側と同じ扱い）。
# この端末 (AUTOLOG_DEVICE) は常に対象へ含める。
$devices = Get-GkillTargetDevices $settings
if (-not $devices) { throw "対象の端末が決まりません。AUTOLOG_DEVICE を $EnvFile に書いてください" }

Write-Host "接続先: $base"
Write-Host '確認するユーザー:'
$devices | ForEach-Object { Write-Host "  $prefix$_" }
Write-Host ''

if (-not (Test-GkillReachable $base)) {
    throw "gkill に接続できません ($base)。gkill_server が動いているか確認してください"
}

$hash = Read-GkillPasswordSha256 '端末別ユーザー共通のパスワード'

# 入れる前に、そのパスワードで全ユーザーへログインできるか確かめる。
# 間違ったまま保存すると、取り込みが静かに失敗し続ける。
Write-Host ''
Write-Host '=== ログイン確認 ==='
$failed = @()
foreach ($device in $devices) {
    $user = "$prefix$device"
    try {
        $login = Invoke-GkillApi $base '/api/login' @{
            user_id = $user; password_sha256 = $hash; locale_name = 'ja'
        }
        if ($login.session_id) {
            Write-Host "  $user : OK"
        } else {
            Write-Host "  $user : 拒否されました ($(Get-GkillError $login))"
            $failed += $user
        }
    } catch {
        Write-Host "  $user : $_"
        $failed += $user
    }
}

if ($failed.Count -gt 0) {
    Write-Host ''
    Write-Host 'ログインできないユーザーがあるため保存しません。'
    Write-Host 'setup_auto_users.ps1 で設定したパスワードと同じものを入力してください。'
    Write-Host '作成されていない場合は先に setup_auto_users.ps1 を実行してください。'
    exit 1
}

Set-GkillEnvValue $EnvFile 'GKILL_AUTO_PASSWORD_SHA256' $hash

Write-Host ''
Write-Host "設定しました: $EnvFile"
Write-Host "  GKILL_AUTO_PASSWORD_SHA256=$($hash.Substring(0,8))…（$($hash.Length)文字）"
Write-Host ''
Write-Host '次は取り込む内容を確認してください:'
Write-Host '  .\src\scripts\run_import.ps1 -DryRun -UntilNow'
