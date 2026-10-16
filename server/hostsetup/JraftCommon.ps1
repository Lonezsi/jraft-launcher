# Shared pieces for the JRAFT Minecraft server scripts on the host.
#
# Layout under the root (H:\jraft-server):
#   hostsetup\   these scripts, copied from the jraft-launcher repository's server\hostsetup
#   java\        the Java runtime the server runs on, downloaded and kept here
#   server\      the Minecraft server itself: world, mods, configs, logs
#   data\        this machinery's own files: its log, the RCON password, the mod state

$Root      = Split-Path -Parent $PSScriptRoot
$DataDir   = Join-Path $Root 'data'
$ServerDir = Join-Path $Root 'server'
$JavaDir   = Join-Path $Root 'java'
New-Item -ItemType Directory -Force -Path $DataDir, $ServerDir, $JavaDir | Out-Null

$Log       = Join-Path $DataDir 'host-minecraft.log'
$Repo      = 'Lonezsi/jraft-launcher'
$GamePort  = 25565
$RconPort  = 25575
$Minecraft = '26.3'
$JavaMajor = 25

# Memory for the server. The host has 32 GB and also runs a language model for Kairo,
# so the server gets a generous but bounded share.
$HeapMin = '4G'
$HeapMax = '10G'

function Say([string]$text) {
    if ((Test-Path $Log) -and ((Get-Item $Log).Length -gt 1MB)) {
        Move-Item -Path $Log -Destination "$Log.1" -Force -ErrorAction SilentlyContinue
    }
    Add-Content -Path $Log -Value ("{0}  {1}" -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $text) -Encoding utf8
}

# TLS 1.2 explicitly: Windows PowerShell 5.1 still defaults to older protocols, which
# GitHub and Modrinth's CDN refuse.
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

function Get-Url([string]$url, [string]$dest) {
    $tmp = "$dest.part"
    for ($try = 1; $try -le 4; $try++) {
        try {
            Invoke-WebRequest -Uri $url -OutFile $tmp -UseBasicParsing -TimeoutSec 600 `
                -Headers @{ 'User-Agent' = 'Lonezsi/jraft-server' } -ErrorAction Stop
            Move-Item -Path $tmp -Destination $dest -Force -ErrorAction Stop
            return
        } catch {
            if ($try -eq 4) { throw }
            Start-Sleep -Seconds (2 * $try)
        }
    }
}

function Get-Json([string]$url) {
    $r = Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 60 `
        -Headers @{ 'User-Agent' = 'Lonezsi/jraft-server' } -ErrorAction Stop
    return ($r.Content | ConvertFrom-Json)
}

function Sha512([string]$path) {
    return (Get-FileHash -Path $path -Algorithm SHA512).Hash.ToLowerInvariant()
}

# ── RCON ─────────────────────────────────────────────────────────────────────
# Minecraft's remote console, which is how the scheduled jobs talk to a server that
# has no window. Listening on 127.0.0.1 only, with a random password kept in data\.

$RconPasswordFile = Join-Path $DataDir 'rcon-password.txt'

function Get-RconPassword {
    if (-not (Test-Path $RconPasswordFile)) {
        $bytes = New-Object byte[] 24
        [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
        $pw = [Convert]::ToBase64String($bytes) -replace '[^A-Za-z0-9]', 'x'
        Set-Content -Path $RconPasswordFile -Value $pw -Encoding ascii -NoNewline
    }
    return (Get-Content -Path $RconPasswordFile -Raw).Trim()
}

function Send-RconPacket($stream, [int]$id, [int]$type, [string]$body) {
    $payload = [Text.Encoding]::UTF8.GetBytes($body)
    $len = 4 + 4 + $payload.Length + 2
    $buf = New-Object byte[] (4 + $len)
    [BitConverter]::GetBytes([int]$len).CopyTo($buf, 0)
    [BitConverter]::GetBytes($id).CopyTo($buf, 4)
    [BitConverter]::GetBytes($type).CopyTo($buf, 8)
    $payload.CopyTo($buf, 12)
    $stream.Write($buf, 0, $buf.Length)
}

function Read-RconPacket($stream) {
    $head = New-Object byte[] 4
    $got = 0
    while ($got -lt 4) { $n = $stream.Read($head, $got, 4 - $got); if ($n -le 0) { throw 'rcon closed' }; $got += $n }
    $len = [BitConverter]::ToInt32($head, 0)
    $rest = New-Object byte[] $len
    $got = 0
    while ($got -lt $len) { $n = $stream.Read($rest, $got, $len - $got); if ($n -le 0) { throw 'rcon closed' }; $got += $n }
    return [pscustomobject]@{
        Id   = [BitConverter]::ToInt32($rest, 0)
        Body = [Text.Encoding]::UTF8.GetString($rest, 8, [Math]::Max(0, $len - 10))
    }
}

# Runs one console command and returns what the server answered.
function Invoke-Rcon([string]$command) {
    $client = New-Object Net.Sockets.TcpClient
    try {
        $client.ReceiveTimeout = 15000
        $client.Connect('127.0.0.1', $RconPort)
        $s = $client.GetStream()
        Send-RconPacket $s 1 3 (Get-RconPassword)
        $auth = Read-RconPacket $s
        if ($auth.Id -eq -1) { throw 'rcon password was refused' }
        Send-RconPacket $s 2 2 $command
        return (Read-RconPacket $s).Body
    } finally { $client.Close() }
}

function Test-ServerAnswering {
    try { Invoke-Rcon 'list' | Out-Null; return $true } catch { return $false }
}

function Get-ServerProcess {
    $mine = [regex]::Escape($ServerDir)
    Get-CimInstance Win32_Process -Filter "Name like 'java%'" -ErrorAction SilentlyContinue |
        Where-Object { $_.CommandLine -and $_.CommandLine -match 'fabric-server-launch' -and
                       ($_.CommandLine -match $mine -or $_.ExecutablePath -like "$JavaDir*") }
}

# How many players are on, from "There are N of a max of M players online".
function Get-PlayerCount {
    $answer = Invoke-Rcon 'list'
    if ($answer -match 'There are (\d+)') { return [int]$Matches[1] }
    return 0
}
