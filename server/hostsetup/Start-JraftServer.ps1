# Start the JRAFT Minecraft server on a machine nobody is sitting at, and keep it started.
#
# Like Foyer's and JR!TER's start scripts, this is both the launcher and the watchdog. It
# runs at boot and every few minutes: when the server is answering it does nothing, and
# when it is not, it brings everything up to date and starts it.
#
# "Answering" means RCON replies, not that a java process exists: a server can hang
# while still holding its port, and to a player that is the same as it being down.
#
# Mods are only synced right before a start, because a running server's jars cannot be
# replaced. A newer server/lock.json on GitHub is picked up at the next restart, which the
# nightly maintenance job does when nobody is online.

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'JraftCommon.ps1')

$StartedFile = Join-Path $DataDir 'started-at.txt'
# A modded server takes a few minutes to start on this laptop, more on the first run
# while it generates spawn. Until then "not answering" is expected, not a fault.
$StartupGrace = [TimeSpan]::FromMinutes(8)

$proc = Get-ServerProcess
if ($proc) {
    if (Test-ServerAnswering) { exit 0 }
    $since = if (Test-Path $StartedFile) { [datetime](Get-Content $StartedFile -Raw) } else { [datetime]::MinValue }
    if ((Get-Date) - $since -lt $StartupGrace) { exit 0 }
    Say "server process $($proc.ProcessId) is not answering RCON after the startup grace period; stopping it"
    foreach ($p in @($proc)) { try { Stop-Process -Id $p.ProcessId -Force -ErrorAction Stop } catch { Say "  could not stop it: $($_.Exception.Message)" } }
    Start-Sleep -Seconds 5
}

# The Minecraft EULA has to be accepted by a person, so it is never written here. The
# setup script writes it once the owner has said yes.
if (-not (Test-Path (Join-Path $ServerDir 'eula.txt')) -or
    -not ((Get-Content (Join-Path $ServerDir 'eula.txt') -Raw) -match 'eula=true')) {
    Say "not starting: server\eula.txt does not say eula=true yet"
    exit 0
}

try {
    # ── Java ──────────────────────────────────────────────────────────────────
    $java = Get-ChildItem -Path $JavaDir -Recurse -Filter 'java.exe' -ErrorAction SilentlyContinue |
        Where-Object { $_.Directory.Name -eq 'bin' } | Select-Object -First 1
    if (-not $java) {
        Say "downloading Java $JavaMajor"
        $asset = (Get-Json "https://api.adoptium.net/v3/assets/latest/$JavaMajor/hotspot?os=windows&architecture=x64&image_type=jre")[0].binary.package
        $zip = Join-Path $JavaDir $asset.name
        Get-Url $asset.link $zip
        if ((Get-FileHash -Path $zip -Algorithm SHA256).Hash.ToLowerInvariant() -ne $asset.checksum) { throw 'the Java download was damaged' }
        Expand-Archive -Path $zip -DestinationPath $JavaDir -Force
        Remove-Item $zip -Force
        $java = Get-ChildItem -Path $JavaDir -Recurse -Filter 'java.exe' | Where-Object { $_.Directory.Name -eq 'bin' } | Select-Object -First 1
    }

    # ── the pack's server mod list ────────────────────────────────────────────
    $lock = Get-Json ("https://raw.githubusercontent.com/$Repo/main/server/lock.json?t=" + [DateTimeOffset]::UtcNow.ToUnixTimeSeconds())

    # ── Fabric's server launcher for the lock's Minecraft and loader ─────────
    $launcher = Join-Path $ServerDir 'fabric-server-launch.jar'
    $launcherFor = Join-Path $DataDir 'launcher-version.txt'
    $want = "$($lock.minecraft)|$($lock.loaderVersion)"
    if (-not (Test-Path $launcher) -or -not (Test-Path $launcherFor) -or (Get-Content $launcherFor -Raw).Trim() -ne $want) {
        $installer = (Get-Json 'https://meta.fabricmc.net/v2/versions/installer' | Where-Object { $_.stable } | Select-Object -First 1).version
        Say "downloading the Fabric server for Minecraft $($lock.minecraft), loader $($lock.loaderVersion)"
        Get-Url "https://meta.fabricmc.net/v2/versions/loader/$($lock.minecraft)/$($lock.loaderVersion)/$installer/server/jar" $launcher
        Set-Content -Path $launcherFor -Value $want -Encoding ascii
    }

    # ── mods ──────────────────────────────────────────────────────────────────
    # Same rule as the client: files the pack put here are kept in step with the lock,
    # and anything somebody added by hand is left alone.
    $modsDir = Join-Path $ServerDir 'mods'
    New-Item -ItemType Directory -Force -Path $modsDir | Out-Null
    $stateFile = Join-Path $DataDir 'mods-state.txt'
    $previous = if (Test-Path $stateFile) { Get-Content $stateFile } else { @() }
    $wanted = @()
    foreach ($f in $lock.files) {
        if (-not $f.path.StartsWith('mods/')) { continue }
        $name = $f.path.Substring(5)
        if ($name -match '[\\/:]' -or $name -match '\.\.') { throw "unsafe path in the lock: $($f.path)" }
        $wanted += $name
        $dest = Join-Path $modsDir $name
        if ((Test-Path $dest) -and (Sha512 $dest) -eq $f.sha512) { continue }
        Say "  downloading $name"
        Get-Url $f.url $dest
        if ((Sha512 $dest) -ne $f.sha512) { Remove-Item $dest -Force; throw "checksum mismatch for $name" }
    }
    foreach ($old in $previous) {
        if ($old -and ($wanted -notcontains $old) -and (Test-Path (Join-Path $modsDir $old))) {
            Say "  removing $old, which is no longer in the pack"
            Remove-Item (Join-Path $modsDir $old) -Force
        }
    }
    Set-Content -Path $stateFile -Value $wanted -Encoding utf8

    # ── server.properties ─────────────────────────────────────────────────────
    # Written in full the first time from the template next to these scripts; after that
    # only the lines this machinery depends on are enforced, so other settings changed by
    # hand stay changed.
    $props = Join-Path $ServerDir 'server.properties'
    if (-not (Test-Path $props)) { Copy-Item (Join-Path $PSScriptRoot 'server.properties') $props }
    $enforce = [ordered]@{
        # Only this machine can reach the server directly. Players come in through
        # Tailscale Funnel, which connects to it locally.
        'server-ip'         = '127.0.0.1'
        'server-port'       = "$GamePort"
        'enable-rcon'       = 'true'
        'rcon.port'         = "$RconPort"
        'rcon.password'     = (Get-RconPassword)
        'online-mode'       = 'true'
        'white-list'        = 'true'
        'enforce-whitelist' = 'true'
    }
    $lines = Get-Content $props
    foreach ($k in $enforce.Keys) {
        $hit = $false
        $lines = foreach ($l in $lines) {
            if ($l -match "^$([regex]::Escape($k))=") { $hit = $true; "$k=$($enforce[$k])" } else { $l }
        }
        if (-not $hit) { $lines += "$k=$($enforce[$k])" }
    }
    Set-Content -Path $props -Value $lines -Encoding ascii

    # ── start ─────────────────────────────────────────────────────────────────
    $jvmArgs = @(
        "-Xms$HeapMin", "-Xmx$HeapMax",
        # ZGC keeps pauses short at this heap size, which is what players notice.
        '-XX:+UseZGC', '-XX:+AlwaysPreTouch', '-XX:+DisableExplicitGC',
        '-Dfile.encoding=UTF-8',
        '-jar', 'fabric-server-launch.jar', 'nogui'
    )
    Say "starting the server (Java at $($java.FullName))"
    Start-Process -FilePath $java.FullName -ArgumentList $jvmArgs -WorkingDirectory $ServerDir -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $DataDir 'console.log') -RedirectStandardError (Join-Path $DataDir 'console-error.log')
    Set-Content -Path $StartedFile -Value (Get-Date -Format 'o') -Encoding ascii
} catch {
    Say "could not start the server: $($_.Exception.Message)"
    exit 1
}
