# 端末別の自動ログ用ユーザーを作る。
#
#   <接頭辞><端末名>  例: myuser_auto_Laptop
#
# rep は初期化処理が作る既定の場所（$GKILL_HOME/datas/<user>/）のまま使う。
# そこから先へ運ぶのは同期スクリプトの役目で、
# アカウント名から端末名を取り出して dvnf copy --device で運ぶ。
#
# パスワードは画面に出さず、その場でハッシュへ変換して破棄する。
# Windows PowerShell 5.1 と PowerShell 7 の両方で動く。

[CmdletBinding()]
param(
    [string]$BaseUrl = 'https://127.0.0.1:9999',
    # gkill の管理者アカウント名。ユーザーの追加には管理者が要る。
    # 既定値は置かない（利用者名をコードに書かない）。
    [Parameter(Mandatory)][string]$AdminUser,
    [Parameter(Mandatory)][string]$UserPrefix,
    [Parameter(Mandatory)][string[]]$Devices,
    # 何をするかだけ表示して、実際には作らない。
    [switch]$WhatIfOnly,
    # 証明書の検証を省く。GKILL_INSECURE (環境変数か autolog.env) でも指定できる。
    [switch]$Insecure
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '_gkill_api.ps1')

# 証明書の検証は明示されたときだけ省く (他のスクリプトと同じ扱い)。
# 以前はこのスクリプトだけ無条件にオフで、-BaseUrl に LAN 越しの gkill を
# 指定した場合も管理者パスワードのハッシュを検証なしで送っていた。
$envFile = Join-Path $PSScriptRoot 'autolog.env'
$settings = if (Test-Path $envFile) { Read-GkillEnvFile $envFile } else { @{} }
if ($Insecure -or (Test-GkillInsecure $settings)) { Enable-GkillInsecureTls }

# ---------------------------------------------------------------- 事前確認

Write-Host '=== 事前確認 ==='
Write-Host ("  PowerShell  : {0}" -f $PSVersionTable.PSVersion)

$accountDb = Join-Path $env:USERPROFILE 'gkill\configs\account.db'
if (-not (Test-Path $accountDb)) { throw "アカウントDBが見つかりません: $accountDb" }
Write-Host '  アカウントDB: あり'

if (-not (Get-Command sqlite3.exe -ErrorAction SilentlyContinue)) {
    throw 'sqlite3.exe が PATH にありません'
}
Write-Host '  sqlite3     : あり'

if (-not (Test-GkillReachable $BaseUrl)) {
    throw "gkill に接続できません ($BaseUrl)。gkill_server が動いているか確認してください"
}
Write-Host "  gkill       : 応答あり ($BaseUrl)"

# sqlite3 へ渡す値は ' を '' に畳み、名前に引用符が混ざってもクエリを壊さない。
$adminUserSql = $AdminUser -replace "'", "''"
$adminRow = (& sqlite3.exe $accountDb "SELECT IS_ADMIN FROM ACCOUNT WHERE USER_ID='$adminUserSql';" | Out-String).Trim()
if ($adminRow -eq '') { throw "アカウント '$AdminUser' が存在しません。-AdminUser で指定してください" }
if ($adminRow -ne '1') { throw "アカウント '$AdminUser' は管理者ではありません。ユーザーの追加には管理者が要ります" }
Write-Host "  管理者      : $AdminUser"

# ---------------------------------------------------------------- 管理者ログイン

Write-Host ''
$adminPw = Read-GkillPasswordSha256 "管理者 ($AdminUser) のパスワード"
$admin = Invoke-GkillApi $BaseUrl '/api/login' @{
    user_id = $AdminUser; password_sha256 = $adminPw; locale_name = 'ja'
}
if (Test-GkillRateLimited $admin) {
    throw 'ログイン試行の回数制限に当たりました (IP ごとに15分で10回、成功も数えられます)。15分ほど待ってからやり直してください'
}
$adminError = Get-GkillError $admin
if ($adminError -or -not $admin.session_id) {
    throw "管理者でログインできませんでした: $adminError"
}
Write-Host '管理者でログインしました'

# 作るユーザーを確定してから、2つめのパスワードを聞く。
$plan = foreach ($device in $Devices) {
    $user = "$UserPrefix$device"
    $userSql = $user -replace "'", "''"
    $exists = (& sqlite3.exe $accountDb "SELECT 1 FROM ACCOUNT WHERE USER_ID='$userSql';" | Out-String).Trim() -eq '1'
    [pscustomobject]@{ User = $user; Device = $device; Exists = $exists }
}

Write-Host ''
Write-Host '=== 作成するユーザー ==='
foreach ($item in $plan) {
    $state = if ($item.Exists) { '既にあり（パスワードのみ設定を試みる）' } else { '新規作成' }
    Write-Host ("  {0,-24} {1}" -f $item.User, $state)
}

if ($WhatIfOnly) {
    Write-Host ''
    Write-Host '(-WhatIfOnly のため何もしません)'
    return
}

Write-Host ''
$autoPw = Read-GkillPasswordSha256 '自動ログ用ユーザーに設定するパスワード（全端末共通）'

# ---------------------------------------------------------------- 作成

foreach ($item in $plan) {
    $user = $item.User
    Write-Host ''
    Write-Host "--- $user ---"

    if (-not $item.Exists) {
        # do_initialize で既定の rep も一緒に作られる。場所は既定のまま。
        $created = Invoke-GkillApi $BaseUrl '/api/add_user' @{
            session_id    = $admin.session_id
            do_initialize = $true
            locale_name   = 'ja'
            account_info  = @{ user_id = $user; is_admin = $false; is_enable = $true }
        }
        $createError = Get-GkillError $created
        if ($createError) { throw "作成に失敗しました: $createError" }
        Write-Host '  作成しました（既定の rep も生成）'
    } else {
        Write-Host '  既にあるので作成はとばします'
    }

    # 作成時に発行される reset_token でパスワードを設定する。
    # 設定済みのユーザーは token が空になる。
    $userSql = $user -replace "'", "''"
    $token = (& sqlite3.exe $accountDb "SELECT PASSWORD_RESET_TOKEN FROM ACCOUNT WHERE USER_ID='$userSql';" | Out-String).Trim()
    if ($token) {
        $reset = Invoke-GkillApi $BaseUrl '/api/set_new_password' @{
            user_id             = $user
            reset_token         = $token
            new_password_sha256 = $autoPw
            locale_name         = 'ja'
        }
        $resetError = Get-GkillError $reset
        if ($resetError) { throw "パスワード設定に失敗しました: $resetError" }
        Write-Host '  パスワードを設定しました'
    } else {
        Write-Host '  パスワード設定済みのためとばします'
    }

    # ログインできるか、rep が見えるかを確認する。
    $check = Invoke-GkillApi $BaseUrl '/api/login' @{
        user_id = $user; password_sha256 = $autoPw; locale_name = 'ja'
    }
    if (Test-GkillRateLimited $check) {
        throw ('ログイン試行の回数制限に当たりました (IP ごとに15分で10回、成功も数えられます)。' +
               'ユーザーの作成とパスワード設定は済んでいるので、15分ほど待ってから再実行すると残りを確認できます')
    }
    $checkError = Get-GkillError $check
    if ($checkError -or -not $check.session_id) {
        throw ("作成したユーザーでログインできません: $checkError" + [Environment]::NewLine +
               '  （既にあるユーザーの場合、以前と違うパスワードを入力した可能性があります）')
    }
    $reps = Invoke-GkillApi $BaseUrl '/api/get_all_rep_names' @{
        session_id = $check.session_id; locale_name = 'ja'
    }
    Write-Host "  ログイン確認 OK / rep $($reps.rep_names.Count) 個"
    Write-Host "  書き込み先: $env:USERPROFILE\gkill\datas\$user\"
}

# ---------------------------------------------------------------- 案内

Write-Host ''
Write-Host '=== 完了 ==='
Write-Host ''
Write-Host 'このあとの手順:'
Write-Host '  1. autolog.env に GKILL_AUTO_USER_PREFIX と GKILL_AUTO_PASSWORD_SHA256 を入れる'
Write-Host '  2. 同期スクリプト を流す（Auto*_<端末>_<日付>.db が Kyou に現れる）'
Write-Host '  3. 閲覧用ユーザーの設定にリポジトリを4本追加する（読み取り専用）'
foreach ($pair in @(
        @('timeis', 'AutoTimeIs'), @('urlog', 'AutoURLog'),
        @('kmemo', 'AutoKmemo'), @('tag', 'AutoTag'))) {
    Write-Host ("       type={0,-7} file=`$HOME/Kyou/{1}_*" -f $pair[0], $pair[1])
}
Write-Host '       use_to_write=オフ  is_execute_idf_when_reload=オフ  is_enable=オン'
Write-Host ''
Write-Host '     ※ use_to_write は必ずオフ。オンにすると同じ型の書き込み先が二重になり'
Write-Host '        gkill が起動時にエラーになります。'
Write-Host '  4. gkill_server を再起動する'
