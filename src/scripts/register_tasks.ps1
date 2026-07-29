# gkill_autolog をタスクスケジューラへ登録する。
#
#   gkill_autolog_collect   ログオン時に常駐収集を開始する
#   gkill_autolog_import    毎日午前4時に取り込みを実行する
#
# 同期スクリプトから取り込みを呼んでいるなら、後者は不要。
#
# 収集はサービスではなくログオン時のタスクにする。
# 前面ウィンドウとセッション状態は、対話セッションに属するプロセスからしか取れないため。
#
# 管理者権限は不要（自分のアカウントのタスクとして登録する）。

[CmdletBinding()]
param(
    # 登録せず、設定内容だけを表示する。
    [switch]$WhatIfOnly,
    # 登録済みのタスクを削除する。
    [switch]$Unregister
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '_gkill_api.ps1')

$repoRoot = Get-AutologRoot $PSScriptRoot
# npm run build の出力先。昔はリポジトリ直下だったのでそちらも見る。
$autolog = @(
    (Join-Path $repoRoot 'release/windows_amd64/autolog.exe')
    (Join-Path $repoRoot 'autolog.exe')
) | Where-Object { Test-Path $_ } | Select-Object -First 1
$runImport = Join-Path $PSScriptRoot 'run_import.ps1'

$collectTaskName = 'gkill_autolog_collect'
$importTaskName = 'gkill_autolog_import'

if ($Unregister) {
    foreach ($name in @($collectTaskName, $importTaskName)) {
        if (Get-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue) {
            Unregister-ScheduledTask -TaskName $name -Confirm:$false
            Write-Host "削除した: $name"
        } else {
            Write-Host "存在しない: $name"
        }
    }
    exit 0
}

if (-not $autolog -or -not (Test-Path $autolog)) {
    throw 'autolog.exe が見つからない。npm run build を実行してください'
}
if (-not (Test-Path $runImport)) {
    throw "run_import.ps1 が見つからない: $runImport"
}

Write-Host "autolog.exe : $autolog"
Write-Host "run_import  : $runImport"
Write-Host ''

# ---------------------------------------------------------------- 常駐収集

$collectAction = New-ScheduledTaskAction -Execute $autolog -Argument 'collect' -WorkingDirectory $repoRoot
$collectTrigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$collectSettings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -DontStopOnIdleEnd `
    -ExecutionTimeLimit ([TimeSpan]::Zero) `
    -RestartCount 3 `
    -RestartInterval (New-TimeSpan -Minutes 1)

# 実行条件は必ず明示する。
#
# 省略すると、登録したときの状況しだいで「ユーザーがログオンしているか
# どうかにかかわらず実行する」(LogonType=Password) になることがある。
# そうなるとタスクはセッション0で動き、入力デスクトップを開けなくなるため、
# スクリーンショットも前面ウィンドウも記録されなくなる。しかもエラーには
# ならず、ロック中と判定されて静かに撮影が飛ばされ続ける。
# 実際にこれで2日ぶん記録が止まった。
#
# 黒いコンソールウィンドウは autolog collect が自分で隠すので、
# 窓を消したいという理由でこの設定を変えてはいけない。
#
# 管理者権限は要らない。画面の取得にも前面ウィンドウの取得にも不要。
$collectPrincipal = New-ScheduledTaskPrincipal `
    -UserId "$env:USERDOMAIN\$env:USERNAME" `
    -LogonType Interactive `
    -RunLevel Limited

# ---------------------------------------------------------------- 取り込み

$importAction = New-ScheduledTaskAction `
    -Execute 'powershell.exe' `
    -Argument "-NoProfile -ExecutionPolicy Bypass -File `"$runImport`"" `
    -WorkingDirectory $repoRoot
$importTrigger = New-ScheduledTaskTrigger -Daily -At '4:00AM'
$importSettings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -StartWhenAvailable `
    -ExecutionTimeLimit (New-TimeSpan -Hours 2)

if ($WhatIfOnly) {
    Write-Host "$collectTaskName : ログオン時に `"$autolog collect`" (対話セッション・非昇格)"
    Write-Host "$importTaskName : 毎日 04:00 に `"$runImport`""
    Write-Host ''
    Write-Host '(-WhatIfOnly のため登録しない)'
    exit 0
}

Register-ScheduledTask -TaskName $collectTaskName -Action $collectAction -Trigger $collectTrigger `
    -Principal $collectPrincipal `
    -Settings $collectSettings -Description 'gkill_autolog: 操作ログの常駐収集' -Force | Out-Null
Write-Host "登録した: $collectTaskName (ログオン時・対話セッション)"

# StartWhenAvailable により、4時に電源が入っていなくても起動後に実行される。
Register-ScheduledTask -TaskName $importTaskName -Action $importAction -Trigger $importTrigger `
    -Settings $importSettings -Description 'gkill_autolog: 取り込み' -Force | Out-Null
Write-Host "登録した: $importTaskName (毎日 04:00)"

Write-Host ''
Write-Host '確認:'
Write-Host "  Get-ScheduledTask -TaskName gkill_autolog_*"
Write-Host "  Start-ScheduledTask -TaskName $importTaskName   # 手動実行"
