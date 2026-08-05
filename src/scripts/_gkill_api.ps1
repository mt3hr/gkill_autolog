# gkill の API を叩くための共通処理。他のスクリプトからドットソースで読み込む。
#
#   . (Join-Path $PSScriptRoot '_gkill_api.ps1')
#
# Windows PowerShell 5.1 と PowerShell 7 の両方で動くようにしてある。
# 5.1 には -SkipCertificateCheck が無いため、証明書の検証はコールバックで無効化する。

$script:GkillIsPS5 = $PSVersionTable.PSVersion.Major -lt 6

# 証明書検証を省くかどうか。Enable-GkillInsecureTls を呼んだときだけ真になる。
# 5.1 と 7 で意味が揃うようにする（以前は 7 だと設定に関係なく常に省いていた）。
$script:GkillSkipCertCheck = $false

# Get-AutologRoot はリポジトリのルートを返す。
# スクリプトは src/scripts/ にあるので2階層上がる。
function Get-AutologRoot([string]$ScriptRoot) {
    return (Split-Path (Split-Path $ScriptRoot -Parent) -Parent)
}

# Enable-GkillInsecureTls は自己署名証明書の gkill へ繋げるようにする。
# 5.1 ではプロセス全体の設定になるため、スクリプトの実行中だけの影響で済むよう
# 呼び出しは各スクリプトの冒頭1回にとどめる。
function Enable-GkillInsecureTls {
    $script:GkillSkipCertCheck = $true
    if (-not $script:GkillIsPS5) { return }

    # 5.1 は既定で TLS1.0 のことがあるので明示する。
    [Net.ServicePointManager]::SecurityProtocol =
        [Net.SecurityProtocolType]::Tls12 -bor [Net.SecurityProtocolType]::Tls11 -bor [Net.SecurityProtocolType]::Tls
    [Net.ServicePointManager]::ServerCertificateValidationCallback = { $true }
}

# Test-GkillReachable は gkill が待ち受けているかを TCP だけで確かめる。
# HTTP を使わないので PowerShell のバージョン差の影響を受けない。
function Test-GkillReachable([string]$BaseUrl, [int]$TimeoutMs = 5000) {
    $uri = [Uri]$BaseUrl
    $port = if ($uri.Port -gt 0) { $uri.Port } else { if ($uri.Scheme -eq 'https') { 443 } else { 80 } }

    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $async = $client.BeginConnect($uri.Host, $port, $null, $null)
        if (-not $async.AsyncWaitHandle.WaitOne($TimeoutMs)) { return $false }
        $client.EndConnect($async)
        return $true
    } catch {
        return $false
    } finally {
        $client.Close()
    }
}

# Invoke-GkillApi は gkill の API を叩く。
# 失敗したときに何が起きたか分かるよう、状態と本文をそのまま見せる。
function Invoke-GkillApi([string]$BaseUrl, [string]$Path, [hashtable]$Body) {
    $json = $Body | ConvertTo-Json -Depth 8

    # 本文は UTF-8 のバイト列にしてから渡す。
    # 5.1 の Invoke-RestMethod は文字列の本文を既定のエンコーディングで送るため、
    # 日本語の端末名・ユーザー名が `???` に潰れて届く (7 では化けないので
    # 開発環境では気づけない)。バイト列ならどちらでもそのまま届く。
    $bytes = [Text.Encoding]::UTF8.GetBytes($json)

    $arguments = @{
        Uri         = "$BaseUrl$Path"
        Method      = 'Post'
        ContentType = 'application/json; charset=utf-8'
        Body        = $bytes
    }
    # 7 以降は引数で証明書検証を省ける。5.1 は Enable-GkillInsecureTls で対応済み。
    # どちらも GKILL_INSECURE を見て Enable-GkillInsecureTls を呼んだときだけ省く。
    if (-not $script:GkillIsPS5 -and $script:GkillSkipCertCheck) { $arguments['SkipCertificateCheck'] = $true }

    try {
        return Invoke-RestMethod @arguments
    } catch {
        $status = $null
        try { $status = [int]$_.Exception.Response.StatusCode } catch {}

        $detail = $null
        if ($_.ErrorDetails -and $_.ErrorDetails.Message) {
            $detail = $_.ErrorDetails.Message
        } elseif ($_.Exception.Response) {
            # 5.1 は応答本文を ErrorDetails に入れてくれないことがある。
            try {
                $stream = $_.Exception.Response.GetResponseStream()
                $reader = New-Object System.IO.StreamReader($stream)
                $detail = $reader.ReadToEnd()
                $reader.Close()
            } catch {}
        }
        if (-not $detail) { $detail = $_.Exception.Message }

        throw "API $Path が失敗しました (HTTP $status): $detail"
    }
}

# Get-GkillError は応答の errors を1行にまとめる。
# gkill は HTTP 200 でも errors に中身を入れることがある。
function Get-GkillError($Response) {
    if (-not $Response) { return $null }
    if (-not $Response.errors) { return $null }
    return ($Response.errors | ForEach-Object { "$($_.error_code) $($_.error_message)" }) -join ' / '
}

# Test-GkillRateLimited はログイン試行の回数制限 (ERR000374) に当たったかを返す。
#
# gkill のログインは IP ごとに15分で10回まで。**成功した試行も数えられる。**
# 制限に当たったあとも端末のループを回し続けると、残りの枠を使い潰して
# 本番の取り込みまで巻き込む (実際に起きた事故)。当たったら即座に止めること。
function Test-GkillRateLimited($Response) {
    if (-not $Response -or -not $Response.errors) { return $false }
    return [bool]($Response.errors | Where-Object { $_.error_code -eq 'ERR000374' })
}

# Get-GkillSetting は設定値を「実環境変数 → autolog.env」の順で返す。
#
# Go 側 (config.Load) と同じ優先順位。ここが逆だと、環境変数で運用している
# 構成で確認スクリプトと本番 (autolog import) が別の設定を見てしまい、
# 「確認は通ったのに本番は別の端末へ書く」ようなずれが起きる。
function Get-GkillSetting([hashtable]$Settings, [string]$Key) {
    $fromEnv = [Environment]::GetEnvironmentVariable($Key)
    if ($fromEnv) { return $fromEnv }
    if ($Settings -and $Settings[$Key]) { return $Settings[$Key] }
    return ''
}

# Test-GkillInsecure は GKILL_INSECURE (環境変数または設定ファイル) が真かを返す。
function Test-GkillInsecure([hashtable]$Settings) {
    return (Get-GkillSetting $Settings 'GKILL_INSECURE') -in @('true', '1', 'yes', 'on')
}

# Get-GkillTargetDevices は端末別ユーザーを確認・設定する対象の端末名を返す。
#
# AUTOLOG_ALLOWED_DEVICES は省略できる。Go 側 (config.resolveAllowedDevices) と
# 同じく、この端末 (AUTOLOG_DEVICE、無ければホスト名) を必ず含める。
# ここが食い違うと、確認スクリプトが実際の書き込み先を確認しないまま
# 「準備できています」と言ってしまう。
function Get-GkillTargetDevices([hashtable]$Settings) {
    $devices = @(((Get-GkillSetting $Settings 'AUTOLOG_ALLOWED_DEVICES') -split ',') |
        ForEach-Object { $_.Trim() } | Where-Object { $_ })

    $local = Get-GkillSetting $Settings 'AUTOLOG_DEVICE'
    if (-not $local) {
        # config.go の defaultDeviceName と同じ落とし方（_ . 空白を除く）。
        $local = $env:COMPUTERNAME -replace '[_.\s]', ''
    }
    if ($local -and $devices -notcontains $local) { $devices += $local }
    return $devices
}

# Read-GkillEnvFile は KEY=VALUE 形式の設定ファイルをハッシュテーブルで返す。
#
# 必ず UTF-8 として読む。Get-Content に任せてはいけない。
# Windows PowerShell 5.1 は BOM の無いファイルを Shift_JIS として読むため、
# 日本語コメントのバイト列が2バイト文字と解釈され、その2バイト目が改行を飲み込んで
# 次の行がコメント行に連結されてしまう（設定が黙って読み落とされる）。
function Read-GkillEnvFile([string]$Path) {
    if (-not (Test-Path $Path)) { throw "設定ファイルが無い: $Path" }

    $settings = @{}
    # BOM があればそれに従い、無ければ UTF-8 として読む。
    foreach ($line in [System.IO.File]::ReadAllLines($Path, [Text.UTF8Encoding]::new($false))) {
        $trimmed = $line.Trim()
        if ($trimmed -eq '' -or $trimmed.StartsWith('#')) { continue }
        $pair = $trimmed -split '=', 2
        if ($pair.Count -eq 2) { $settings[$pair[0].Trim()] = $pair[1].Trim() }
    }
    return $settings
}

# Set-GkillEnvValue は設定ファイルの1項目だけを書き換える。
# 読み書きとも UTF-8(BOM付き)で扱い、5.1 でも読み落とされないようにする。
function Set-GkillEnvValue([string]$Path, [string]$Key, [string]$Value) {
    $lines = [System.IO.File]::ReadAllLines($Path, [Text.UTF8Encoding]::new($false))

    $found = $false
    $updated = foreach ($line in $lines) {
        if ($line -like "$Key=*") { $found = $true; "$Key=$Value" } else { $line }
    }
    if (-not $found) { $updated = @($updated) + "$Key=$Value" }

    # BOM を付けて書く。5.1 の既定エンコーディングに左右されなくなる。
    [System.IO.File]::WriteAllLines($Path, [string[]]$updated, [Text.UTF8Encoding]::new($true))
}

# Read-GkillPasswordSha256 はパスワードを聞いて sha256(16進小文字) を返す。
# 平文は画面に出さず、ハッシュ化後すぐ破棄する。
function Read-GkillPasswordSha256([string]$Prompt) {
    $secure = Read-Host $Prompt -AsSecureString
    $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    $plain = $null
    try {
        $plain = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr)
        if ([string]::IsNullOrEmpty($plain)) { throw 'パスワードが入力されませんでした' }
        $sha = [System.Security.Cryptography.SHA256]::Create()
        try {
            return ($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($plain)) |
                ForEach-Object { $_.ToString('x2') }) -join ''
        } finally { $sha.Dispose() }
    } finally {
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
        $plain = $null
    }
}
