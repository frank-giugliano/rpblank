# Installs rpblank as a hidden logon task (runs as you, no console window).
# Run from the folder containing rpblank.exe:  powershell -ExecutionPolicy Bypass -File install.ps1
# Remove with:  powershell -ExecutionPolicy Bypass -File install.ps1 -Uninstall
param([switch]$Uninstall)
$name = 'rpblank'
$exe  = Join-Path $PSScriptRoot 'rpblank.exe'
Stop-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue
Unregister-ScheduledTask -TaskName $name -Confirm:$false -ErrorAction SilentlyContinue
if ($Uninstall) { Write-Host "rpblank logon task removed."; exit 0 }
if (-not (Test-Path $exe)) { throw "rpblank.exe not found next to this script" }
$action    = New-ScheduledTaskAction -Execute $exe -Argument '-open=false' -WorkingDirectory $PSScriptRoot
$trigger   = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited
$settings  = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::Zero) -StartWhenAvailable
Register-ScheduledTask -TaskName $name -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
Start-ScheduledTask -TaskName $name
Write-Host "rpblank installed and started. Web UI: http://127.0.0.1:47996/"
