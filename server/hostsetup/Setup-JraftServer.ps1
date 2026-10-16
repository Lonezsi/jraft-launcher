# One-time (and safe to repeat) setup of the JRAFT Minecraft server on the host.
#
#   1. Scheduled tasks, all as SYSTEM like the host's other services:
#        JRAFT Minecraft Server   at boot and every 3 minutes: Start-JraftServer.ps1
#        JRAFT Minecraft Night    03:00: apply pending updates, start pre-generation
#        JRAFT Minecraft Morning  10:00: stop pre-generation
#   2. Tailscale Funnel: TLS on the public port 10000, decrypted and handed to jraft-relay
#      on 127.0.0.1:25600, which passes game and voice streams to the server.
#      The websites keep port 443; this adds a port, it does not touch them.
#   3. With -AcceptEula, server\eula.txt. Mojang's EULA has to be agreed to by the person
#      running the server: https://aka.ms/MinecraftEULA
#
# Run elevated.

param([switch]$AcceptEula)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'JraftCommon.ps1')

$ps = "$env:WINDIR\System32\WindowsPowerShell\v1.0\powershell.exe"
function Task([string]$name, [string]$file, [string]$extra, $triggers) {
    $action = New-ScheduledTaskAction -Execute $ps `
        -Argument "-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$(Join-Path $PSScriptRoot $file)`" $extra"
    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    $settings = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -ExecutionTimeLimit ([TimeSpan]::Zero) `
        -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
    Register-ScheduledTask -TaskName $name -Action $action -Trigger $triggers -Principal $principal `
        -Settings $settings -Force | Out-Null
    Say "setup: task '$name' registered"
}

$every3 = New-ScheduledTaskTrigger -Once -At (Get-Date).Date -RepetitionInterval (New-TimeSpan -Minutes 3)
Task 'JRAFT Minecraft Server' 'Start-JraftServer.ps1' '' @((New-ScheduledTaskTrigger -AtStartup), $every3)
Task 'JRAFT Minecraft Night'   'Pregen-JraftServer.ps1' '-Action Night'   (New-ScheduledTaskTrigger -Daily -At '03:00')
Task 'JRAFT Minecraft Morning' 'Pregen-JraftServer.ps1' '-Action Morning' (New-ScheduledTaskTrigger -Daily -At '10:00')

$tailscale = (Get-Command tailscale.exe -ErrorAction SilentlyContinue).Source
if (-not $tailscale) { $tailscale = 'C:\Program Files\Tailscale\tailscale.exe' }
# TLS-terminated, because Funnel's public relays only route TLS: they pick this machine by
# the TLS server name. Players' launchers speak TLS to it (see cmd/jraft/tunnel.go) and
# Tailscale hands the decrypted stream to jraft-relay. A raw --tcp mapping on the same
# port, from an earlier setup, is switched off first.
& $tailscale funnel --tcp=10000 off 2>$null | Out-Null
& $tailscale funnel --bg --tls-terminated-tcp 10000 "tcp://127.0.0.1:$RelayPort"
if ($LASTEXITCODE -ne 0) { throw "tailscale funnel failed with exit code $LASTEXITCODE" }
Say "setup: funnel tls-terminated-tcp 10000 -> jraft-relay on 127.0.0.1:$RelayPort"

if ($AcceptEula) {
    Set-Content -Path (Join-Path $ServerDir 'eula.txt') -Value @(
        '# Accepted by the server owner, see https://aka.ms/MinecraftEULA',
        'eula=true') -Encoding ascii
    Say "setup: eula accepted by the owner"
}

Start-ScheduledTask -TaskName 'JRAFT Minecraft Server'
Say "setup: done"
