# autolog.env の設定で gkill へログインできるか確かめる。
#
# 何も書き込まない。読み取りだけ。
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

$settings = Read-GkillEnvFile $EnvFile

# 実環境変数 → autolog.env の順で見る (Go 側と同じ)。
$base = Get-GkillSetting $settings 'GKILL_BASE_URL'
$user = Get-GkillSetting $settings 'GKILL_USER'
$pw = Get-GkillSetting $settings 'GKILL_PASSWORD_SHA256'

if (-not $base) {
    Write-Host 'GKILL_BASE_URL が未設定です。autolog.env に接続先を書いてください。'
    exit 1
}

Write-Host ("PowerShell: {0}" -f $PSVersionTable.PSVersion)
Write-Host "接続先:     $base"
if (Test-GkillInsecure $settings) {
    Write-Host 'TLS:        証明書の検証を省略 (GKILL_INSECURE)'
    Enable-GkillInsecureTls
}

if (-not (Test-GkillReachable $base)) {
    Write-Host ''
    Write-Host "gkill に接続できません ($base)。gkill_server が動いているか確認してください。"
    exit 1
}

# 単一ユーザー構成のときだけ確認する。
# 端末別ユーザーへ書き分ける構成では GKILL_USER を使わない。
if ($user) {
    if ($pw -notmatch '^[0-9a-f]{64}$') {
        Write-Host ''
        Write-Host "GKILL_PASSWORD_SHA256 が sha256(16進64文字) の形ではありません（$($pw.Length) 文字）。"
        Write-Host 'パスワードそのものではなくハッシュを入れる欄です。set_password.ps1 を実行してください。'
        exit 1
    }
    Write-Host "ユーザ:     $user"
    $login = Invoke-GkillApi $base '/api/login' @{
        user_id = $user; password_sha256 = $pw; locale_name = 'ja'
    }
    $loginError = Get-GkillError $login
    if ($loginError -or -not $login.session_id) {
        Write-Host "ログイン:   拒否されました ($loginError)"
        exit 1
    }
    $reps = Invoke-GkillApi $base '/api/get_all_rep_names' @{
        session_id = $login.session_id; locale_name = 'ja'
    }
    Write-Host "ログイン:   OK (リポジトリ $($reps.rep_names.Count) 個)"
}

# 取り込みの書き込み先は端末別ユーザー。実際に使うのはこちらなので必ず確かめる。
$prefix = Get-GkillSetting $settings 'GKILL_AUTO_USER_PREFIX'
$autoPw = Get-GkillSetting $settings 'GKILL_AUTO_PASSWORD_SHA256'

if (-not $prefix) {
    Write-Host ''
    Write-Host 'GKILL_AUTO_USER_PREFIX が未設定です。'
    exit 1
}
if ([string]::IsNullOrEmpty($autoPw)) {
    Write-Host ''
    Write-Host 'GKILL_AUTO_PASSWORD_SHA256 が空です。先に .\src\scripts\set_auto_password.ps1 を実行してください。'
    exit 1
}

Write-Host ''
Write-Host '=== 端末別ユーザー（取り込みの書き込み先）==='
# AUTOLOG_ALLOWED_DEVICES は省略できる。この端末 (AUTOLOG_DEVICE) は常に確認する。
# 1台も確認せずに「準備できています」と言わないため、決まらなければ失敗にする。
$devices = Get-GkillTargetDevices $settings
if (-not $devices) {
    Write-Host '確認する端末が決まりません。AUTOLOG_DEVICE を設定してください。'
    exit 1
}
$failed = @()
foreach ($device in $devices) {
    $autoUser = "$prefix$device"
    try {
        $check = Invoke-GkillApi $base '/api/login' @{
            user_id = $autoUser; password_sha256 = $autoPw; locale_name = 'ja'
        }
        if ($check.session_id) {
            $autoReps = Invoke-GkillApi $base '/api/get_all_rep_names' @{
                session_id = $check.session_id; locale_name = 'ja'
            }
            Write-Host ("  {0,-24} OK (リポジトリ {1} 個)" -f $autoUser, $autoReps.rep_names.Count)
        } else {
            Write-Host ("  {0,-24} 拒否されました ({1})" -f $autoUser, (Get-GkillError $check))
            $failed += $autoUser
        }
    } catch {
        Write-Host ("  {0,-24} {1}" -f $autoUser, $_)
        $failed += $autoUser
    }
}

if ($failed.Count -gt 0) {
    Write-Host ''
    Write-Host 'ログインできないユーザーがあります。'
    Write-Host 'setup_auto_users.ps1 で作成し、set_auto_password.ps1 でパスワードを設定してください。'
    exit 1
}

Write-Host ''
Write-Host '準備できています。次は書き込まずに内容を確認してください:'
Write-Host '  .\src\scripts\run_import.ps1 -DryRun -UntilNow'
