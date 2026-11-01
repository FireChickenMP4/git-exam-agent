# Windows 内部 CA 代码签名

用于给 `exam-agent.exe` 做 Authenticode 签名，减少 Windows SmartScreen 和杀软对“未知发布者”的拦截。

> 这是**内部 CA**：只有安装了 `root-ca.cer` 的机器才认为签名有效。它不改变公网 SmartScreen 声誉，
> 也不能保证杀软不误报；仍需 T-7 真机验证和误报申诉。

## 1. 生成 CA（只做一次）

在受控构建机上：

```powershell
$env:SIGN_CA_PASSWORD = "pfx 密码"
pwsh -File deploy/signing/new-internal-ca.ps1 `
  -OutDir deploy/signing/certs -Org "示例大学计算机协会"
```

产物：

| 文件 | 用途 | 保管 |
|---|---|---|
| `root-ca.cer` | 根证书，分发给考生机 | 可公开 |
| `root-ca.pfx` | 根证书私钥，续签用 | **离线保存，绝不外发** |
| `code-signing.cer` | 签名证书公钥 | 可公开 |
| `code-signing.pfx` | 签名私钥 | 只放构建机 |

`deploy/signing/certs/` 已在 `.gitignore` 中，不要提交。

## 2. 考生机安装根证书

管理员安装到本机（推荐，一次给所有用户）：

```powershell
pwsh -File deploy/signing/install-root-ca.ps1 -CerPath deploy/signing/certs/root-ca.cer
```

没有管理员权限时只装当前用户：

```powershell
pwsh -File deploy/signing/install-root-ca.ps1 -CerPath ...\root-ca.cer -CurrentUserOnly
```

也可以把 `root-ca.cer` 放进镜像或通过组策略下发。

> 如果考生用**自己的笔记本**，安装你们的根证书属于信任决策，需要提前告知并征得同意；
> 考试结束后可以移除（见文末）。

## 3. 签名

```powershell
$env:SIGN_PFX_PASSWORD = "pfx 密码"
make sign-windows
# 等价于：
pwsh -File deploy/signing/sign-windows.ps1 `
  -Pfx deploy/signing/certs/code-signing.pfx `
  -RootCer deploy/signing/certs/root-ca.cer `
  -Files bin/exam-agent.exe
```

脚本会：

1. 用 PFX 私钥签名；
2. 默认加 DigiCert 公共时间戳（构建机无外网时用 `-Timestamp ""` 关闭）；
3. 调 `Get-AuthenticodeSignature` 校验，状态必须是 `Valid`。

本机实测：签名后 `Get-AuthenticodeSignature` 返回 `Status=Valid`。

验证：

```powershell
Get-AuthenticodeSignature bin\exam-agent.exe | Format-List *
# 或安装 Windows SDK 后：
signtool verify /pa /v bin\exam-agent.exe
```

## 4. 分发检查清单

- 只签名要发给考生的 `exam-agent.exe`；
- 生成 SHA256：`Get-FileHash bin\exam-agent.exe -Algorithm SHA256`，和 exe 一起分发；
- U 盘分发后如带“来自 Internet”标记，可执行 `Unblock-File .\exam-agent.exe`；
- T-7 在代表性机型上实测：签名状态、杀软拦截、agent 能否启动并推送；
- 仍建议向 Microsoft/在用杀软提交样本做误报申诉（签名不能替代）。

## 5. 轮换与移除

- 签名证书默认 3 年，根证书 10 年；到期前用 `root-ca.pfx` 重新签发；
- 私钥泄露时：立即作废旧根、生成新根、重新分发并重签，旧的根从考生机移除；
- 从 Windows 移除内部根证书（管理员）：

```powershell
Get-ChildItem Cert:\LocalMachine\Root |
  Where-Object { $_.Subject -like "*Internal Code Signing Root*" } |
  Remove-Item
```

- 当前用户移除：

```powershell
Get-ChildItem Cert:\CurrentUser\Root |
  Where-Object { $_.Subject -like "*Internal Code Signing Root*" } |
  Remove-Item
```
