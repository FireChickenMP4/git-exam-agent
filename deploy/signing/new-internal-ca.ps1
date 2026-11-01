# 生成考试内部 CA 与代码签名证书
#
# 在受控的构建机上执行一次；根证书私钥要离线保存，不要提交到仓库。
# 用法：
#   $env:SIGN_CA_PASSWORD = "pfx 密码"
#   pwsh -File new-internal-ca.ps1 -OutDir certs -Org "示例大学计算机协会"

[CmdletBinding()]
param(
    [string]$OutDir = "certs",
    [string]$Org = "Exam Tools",
    [int]$RootYears = 10,
    [int]$LeafYears = 3,
    [string]$Password = $env:SIGN_CA_PASSWORD
)

$ErrorActionPreference = "Stop"

if (-not $Password) {
    $secureInput = Read-Host -AsSecureString "PFX 密码"
    $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secureInput)
    try {
        $Password = [Runtime.InteropServices.Marshal]::PtrToStringAuto($bstr)
    } finally {
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
    }
}
$securePassword = ConvertTo-SecureString $Password -AsPlainText -Force

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

Write-Host "生成根证书 ..."
$root = New-SelfSignedCertificate `
    -Type Custom `
    -Subject "CN=$Org Internal Code Signing Root" `
    -KeyUsage CertSign, CRLSign, DigitalSignature `
    -KeyExportPolicy Exportable `
    -KeyLength 4096 `
    -HashAlgorithm SHA256 `
    -CertStoreLocation "Cert:\CurrentUser\My" `
    -NotAfter (Get-Date).AddYears($RootYears) `
    -TextExtension @("2.5.29.19={critical}{text}ca=1&pathlength=1")

Write-Host "生成代码签名证书 ..."
$code = New-SelfSignedCertificate `
    -Type CodeSigningCert `
    -Subject "CN=$Org Exam Agent" `
    -Signer $root `
    -KeyUsage DigitalSignature `
    -KeyExportPolicy Exportable `
    -KeyLength 2048 `
    -HashAlgorithm SHA256 `
    -CertStoreLocation "Cert:\CurrentUser\My" `
    -NotAfter (Get-Date).AddYears($LeafYears)

Export-Certificate -Cert $root -FilePath (Join-Path $OutDir "root-ca.cer") -Type CERT | Out-Null
Export-PfxCertificate -Cert $root -FilePath (Join-Path $OutDir "root-ca.pfx") -Password $securePassword | Out-Null
Export-Certificate -Cert $code -FilePath (Join-Path $OutDir "code-signing.cer") -Type CERT | Out-Null
Export-PfxCertificate -Cert $code -FilePath (Join-Path $OutDir "code-signing.pfx") -Password $securePassword | Out-Null

Write-Host ""
Write-Host "已生成："
Write-Host ("  根证书      {0}\root-ca.cer      thumbprint={1}" -f $OutDir, $root.Thumbprint)
Write-Host ("  根证书 PFX  {0}\root-ca.pfx      （含私钥，离线保存）" -f $OutDir)
Write-Host ("  签名证书    {0}\code-signing.cer  thumbprint={1}" -f $OutDir, $code.Thumbprint)
Write-Host ("  签名 PFX    {0}\code-signing.pfx" -f $OutDir)
Write-Host ""
Write-Host "下一步："
Write-Host "  1. 把 root-ca.cer 安装到每台考生机（管理员）："
Write-Host "     pwsh install-root-ca.ps1 -CerPath $OutDir\root-ca.cer"
Write-Host "  2. 构建后签名："
Write-Host '     $env:SIGN_PFX_PASSWORD = "..."'
Write-Host "     pwsh sign-windows.ps1 -Pfx $OutDir\code-signing.pfx -Files ..\..\bin\exam-agent.exe -RootCer $OutDir\root-ca.cer"
Write-Host "  3. root-ca.pfx 与 code-signing.pfx 不要提交，不要放考生机。"
