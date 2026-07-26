# autolog.env の GKILL_PASSWORD_SHA256 を対話で設定する。
#
# パスワードは画面に出さず、ファイルにも平文では残さない。
# 入力後すぐにハッシュへ変換して破棄する。

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
$hash = Read-GkillPasswordSha256 "gkill ($($settings['GKILL_USER'])) のパスワード"

Set-GkillEnvValue $EnvFile 'GKILL_PASSWORD_SHA256' $hash

Write-Host "設定した: $EnvFile"
Write-Host "  GKILL_PASSWORD_SHA256=$($hash.Substring(0,8))...（$($hash.Length)文字）"
Write-Host ''
Write-Host 'gkill へログインできるか確認する:'
Write-Host '  .\src\scripts\check_connection.ps1'
