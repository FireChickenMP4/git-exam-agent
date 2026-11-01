# 在 Windows 机器上安装考试内部根证书
#
# 管理员（默认装到本机所有用户）：
#   pwsh -File install-root-ca.ps1 -CerPath root-ca.cer
# 仅当前用户（不需要管理员）：
#   pwsh -File install-root-ca.ps1 -CerPath root-ca.cer -CurrentUserOnly

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$CerPath,
    [switch]$CurrentUserOnly
)

$ErrorActionPreference = "Stop"
if (-not (Test-Path $CerPath)) {
    throw "找不到证书：$CerPath"
}

$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
    [Security.Principal.WindowsBuiltInRole]::Administrator)

$store = "Cert:\LocalMachine\Root"
if ($CurrentUserOnly) {
    $store = "Cert:\CurrentUser\Root"
} elseif (-not $isAdmin) {
    throw "安装到本机 Root 需要管理员权限；或加 -CurrentUserOnly 只装当前用户"
}

$cert = Import-Certificate -FilePath $CerPath -CertStoreLocation $store
Write-Host ("已安装到 {0}：thumbprint={1}" -f $store, $cert.Thumbprint)
Write-Host "可以用 Get-AuthenticodeSignature <exe> 检查签名状态。"
