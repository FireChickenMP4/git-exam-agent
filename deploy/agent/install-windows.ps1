# 在考生笔记本上安装 exam-agent：
#   powershell -ExecutionPolicy Bypass -File install-windows.ps1 -WorkDir C:\exam-2027 -AgentExe C:\exam\exam-agent.exe
# 使用当前用户级计划任务，不需要管理员权限。
param(
    [Parameter(Mandatory = $true)][string]$WorkDir,
    [string]$AgentExe = (Join-Path $PSScriptRoot "..\..\bin\exam-agent.exe")
)

$ErrorActionPreference = "Stop"

if (-not (Test-Path $AgentExe)) {
    throw "找不到 exam-agent：$AgentExe"
}
if (-not (Test-Path (Join-Path $WorkDir ".exam-agent.json"))) {
    throw "工作区缺少 .exam-agent.json：$WorkDir"
}

$AgentExe = (Resolve-Path $AgentExe).Path
$WorkDir = (Resolve-Path $WorkDir).Path

$action = New-ScheduledTaskAction -Execute $AgentExe `
    -Argument "-config `"$WorkDir\.exam-agent.json`"" -WorkingDirectory $WorkDir
$trigger = New-ScheduledTaskTrigger -AtLogOn
$settings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -StartWhenAvailable `
    -RestartCount 999 `
    -RestartInterval (New-TimeSpan -Minutes 1) `
    -ExecutionTimeLimit ([TimeSpan]::Zero)

Register-ScheduledTask -TaskName "exam-agent" -Action $action -Trigger $trigger `
    -Settings $settings -Description "考场自动提交与推送" -Force | Out-Null

Start-ScheduledTask -TaskName "exam-agent"
Write-Host "exam-agent 已安装并启动（计划任务：exam-agent）"
