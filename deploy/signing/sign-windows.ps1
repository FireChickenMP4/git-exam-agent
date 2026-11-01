# 用内部 CA 签发的证书给 Windows 可执行文件签名
#
# 用法：
#   $env:SIGN_PFX_PASSWORD = "pfx 密码"
#   pwsh -File sign-windows.ps1 -Pfx certs\code-signing.pfx -Files ..\..\bin\exam-agent.exe -RootCer certs\root-ca.cer

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Pfx,
    [string]$Password = $env:SIGN_PFX_PASSWORD,
    [string[]]$Files = @("bin/exam-agent.exe"),
    [string]$Timestamp = "http://timestamp.digicert.com",
    [string]$RootCer = ""
)

$ErrorActionPreference = "Stop"

if (-not $Password) {
    throw "缺少 PFX 密码：设置环境变量 SIGN_PFX_PASSWORD 或传 -Password"
}
if (-not (Test-Path $Pfx)) {
    throw "找不到 PFX：$Pfx"
}

# 构建机如果没有信任内部根证书，先导入当前用户 Root，便于签名后校验。
if ($RootCer) {
    if (-not (Test-Path $RootCer)) { throw "找不到根证书：$RootCer" }
    Import-Certificate -FilePath $RootCer -CertStoreLocation "Cert:\CurrentUser\Root" | Out-Null
}

$cert = New-Object System.Security.Cryptography.X509Certificates.X509Certificate2($Pfx, $Password)
if (-not $cert.HasPrivateKey) {
    throw "PFX 不包含私钥"
}

$failed = 0
foreach ($file in $Files) {
    if (-not (Test-Path $file)) {
        Write-Host "跳过不存在的文件：$file" -ForegroundColor Yellow
        $failed++
        continue
    }
    $signArgs = @{
        FilePath    = (Resolve-Path $file).Path
        Certificate = $cert
        HashAlgorithm = "SHA256"
    }
    if ($Timestamp) { $signArgs.TimestampServer = $Timestamp }
    Set-AuthenticodeSignature @signArgs | Out-Null

    $check = Get-AuthenticodeSignature $file
    $thumb = $check.SignerCertificate.Thumbprint
    Write-Host ("{0}  status={1}  signer={2}" -f $file, $check.Status, $thumb)
    if ($check.Status -ne "Valid") {
        Write-Host ("  {0}" -f $check.StatusMessage) -ForegroundColor Red
        $failed++
    }
}

if ($failed -gt 0) {
    throw "$failed 个文件签名或校验失败"
}
Write-Host "签名完成" -ForegroundColor Green
