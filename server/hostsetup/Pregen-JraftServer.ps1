# Downtime work for the JRAFT server: run by two scheduled tasks.
#
#   -Action Night    in the small hours. If nobody is online, first apply a pending mod
#                    update (stop the server; the watchdog task syncs mods and starts it
#                    again), and once it is back, start Distant Horizons pre-generating
#                    far terrain around spawn, so players get it sent instead of waiting.
#   -Action Morning  before people play. Stops pre-generation, which uses every core the
#                    server has, so the day is spent on players.
#
# Pre-generation resumes where it stopped: DH skips areas it already has.

param([ValidateSet('Night', 'Morning')][string]$Action = 'Night')

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'JraftCommon.ps1')

# Chunk radius around spawn to pre-generate. 512 chunks is about 8 km each way.
$PregenRadius = 512

if (-not (Test-ServerAnswering)) { Say "${Action}: server is not answering, nothing to do"; exit 0 }

if ($Action -eq 'Morning') {
    try { Say ("morning: dh pregen stop -> " + (Invoke-Rcon 'dh pregen stop').Trim()) }
    catch { Say "morning: could not stop pre-generation: $($_.Exception.Message)" }
    exit 0
}

$players = Get-PlayerCount
if ($players -gt 0) { Say "night: $players player(s) online, leaving the server alone tonight"; exit 0 }

# A newer mod list on GitHub than the one this server started with means a restart is
# due, and the middle of the night with nobody on is the time for it.
try {
    $lock = (Invoke-WebRequest -Uri ("https://raw.githubusercontent.com/$Repo/main/server/lock.json?t=" + [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()) -UseBasicParsing -TimeoutSec 60).Content | ConvertFrom-Json
    $want = @($lock.files | Where-Object { $_.path.StartsWith('mods/') } | ForEach-Object { $_.path.Substring(5) }) | Sort-Object
    $have = @(Get-Content (Join-Path $DataDir 'mods-state.txt') -ErrorAction SilentlyContinue) | Sort-Object
    if (Compare-Object $want $have) {
        Say "night: the mod list changed on GitHub; restarting the server to apply it"
        Invoke-Rcon 'say Restarting for an update, back in a few minutes.' | Out-Null
        Invoke-Rcon 'stop' | Out-Null
        # The watchdog task notices within a few minutes and starts it with the new mods.
        # Pre-generation starts on the next night's run.
        exit 0
    }
} catch { Say "night: could not check for a mod update: $($_.Exception.Message)" }

try {
    $answer = Invoke-Rcon "dh pregen start minecraft:overworld 0 0 $PregenRadius"
    Say ("night: dh pregen start -> " + $answer.Trim())
} catch { Say "night: could not start pre-generation: $($_.Exception.Message)" }
