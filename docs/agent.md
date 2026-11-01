# exam-agent 使用说明

考生本机的自动提交与推送程序。它只用 Go 实现 git 操作（go-git），考场机器不需要安装 Git。

## 行为

1. 在 `workspace` 初始化仓库（默认分支 `main`）并做首次提交。
2. 递归监听工作区变化，安静 3 秒后提交（`debounce_ms`）。
3. 提交后随机等待 10-30 秒再推送（`push_interval_ms_min/max`），把多次提交合并成一次 push。
4. 无改动时按 `heartbeat_ms_min/max`（默认 55-65 秒）创建空提交，证明 agent 仍在运行；工作区有未提交改动时不会制造心跳，避免真实改动被包进心跳提交。心跳可用 `examd review` 过滤。
5. 推送失败进入指数退避：2s、4s、8s……上限 60s，恢复后自动补推。
6. 每 5 秒检查网卡地址变化；无线环境每 30 秒读取一次 SSID，变化时写入 `network_change` 事件。
7. 每 10 秒输出状态行（最后提交、最后推送、失败次数、网络）。
8. 退出（Ctrl+C / 关机）前尽力提交并推送一次。
9. 配置了 `examd_url` 时，事件同时上报 examd，看板显示最后一条 agent 事件。
10. 保存级采样：一次保存让白名单文件增长超过 `bulk_edit_bytes`（默认 4096 字节）时，写一条
    `bulk_edit` 事件（文件、增长字节、当前大小）；只统计字节数，不读内容、不碰剪贴板。
    该值设为负数可禁用。
11. 历史分叉时自动恢复：推送遇到 `non-fast-forward` 时，先把本地 main 备份为
    `agent-backup-<时间>` 分支，再重置到远端 main、重新提交工作区并推送。备份分支只在本地，
    不会上传；复核时以 `push_recover` 事件和推送记录为准。

## 首次初始化（不需要本机 git）

```bash
exam-agent -config .exam-agent.json -init
```

它用内置的 go-git 把仓库拉到 `workspace`；之后常驻或 `-once` 都不需要安装 git。

## 只提交白名单文件

与 pre-receive 钩子一致，只提交：

- `*.go`（含 `vendor/` 下的 Go 源码）
- `go.mod`、`go.sum`、`vendor/modules.txt`
- `.gitignore`

工作区里的 `README.md`、笔记、截图不会提交，状态行会提示被跳过的文件。这是为了避免整次 push 被服务器拒绝。

## 配置

`.exam-agent.json`：

```json
{
  "repo": "http://10.0.0.10:3000/20231234/exam-2027-20231234.git",
  "username": "20231234",
  "token": "<开通时打印的令牌>",
  "workspace": ".",
  "examd_url": "http://10.0.0.10:8080",
  "ca_file": "",
  "insecure_skip_verify": false,
  "bulk_edit_bytes": 4096,
  "debounce_ms": 3000,
  "push_interval_ms_min": 10000,
  "push_interval_ms_max": 30000,
  "heartbeat_ms_min": 55000,
  "heartbeat_ms_max": 65000
}
```

`workspace` 相对路径以配置文件所在目录为基准。
`examd_url` 非空时，agent 会把 `start`/`stop`/`network_change`/`push_fail`/`checkpoint`
事件同时上报到 examd（看板显示最后 agent 事件）；上报失败只丢事件，不影响本地 JSONL 和推送。
`ca_file` 用于自签名 HTTPS 的 Gitea；也可以用 `insecure_skip_verify` 跳过校验（不推荐）。

## 安装

### Windows（推荐）

```powershell
powershell -ExecutionPolicy Bypass -File deploy\agent\install-windows.ps1 `
  -WorkDir C:\exam-2027 -AgentExe C:\exam\exam-agent.exe
```

使用当前用户级计划任务，登录自启；异常退出每分钟重启。

### macOS / Linux

```bash
sudo install -m 0755 bin/exam-agent /usr/local/bin/exam-agent
bash deploy/agent/install.sh /home/student/exam-2027
```

macOS 使用 LaunchAgent，Linux 使用 systemd user unit（`systemctl --user`）。

## 状态与事件

- 状态行示例：

```text
[exam-agent] 14:03:12 status last_commit=14:03:10 last_push=14:02:58 fails=0 pending=false net=Wi-Fi/10.0.0.23
```

- 事件日志：配置文件同目录的 `.exam-agent.events.jsonl`，类型为 `start`、`stop`、`network_change`、`push_fail`。

## Windows 权限与杀软

- `exam-agent.exe` **不需要管理员权限**：只读写考生工作区、发 HTTP 请求、注册当前用户的登录计划任务。
- 如果机房组策略禁止创建计划任务，改用“启动”文件夹快捷方式，或让考生手动运行
  `exam-agent -config ... -once` 打检查点；都不需要提权。
- 未签名的自编译 exe 有被 Defender/第三方杀软启发式误报的风险（文件监听 + 自动提交 + 计划任务
  这类行为容易命中通用规则）。内部 CA 自签的完整步骤见 `deploy/signing/README.md`，优先级：
  1. 用代码签名证书给 `exam-agent.exe` 做 Authenticode 签名并加时间戳；
  2. 考前把样本提交给 Microsoft/在用杀软做误报申诉；
  3. T-7 真机演练时逐台确认；分发用 U 盘并附 SHA256，必要时 `Unblock-File`；
  4. 最后才考虑由管理员给考试目录加排除项，不建议让考生关闭杀软。
- 不要在考场使用 UPX 之类加壳，会显著提高误报率。
- macOS 未签名二进制会被 Gatekeeper 拦；需要签名/公证，或安装后
  `xattr -d com.apple.quarantine /usr/local/bin/exam-agent`。LaunchAgent 不需要 sudo。

## 常见问题

| 现象 | 处理 |
|---|---|
| `push failed ... 401` | 令牌错误或已重置，使用 `examd token` 重新打印 |
| `push failed ... connection refused` | 网络未连上考场 AP；agent 会自动重试 |
| `skipped N non-Go files` | 正常提示，非 Go 文件不会提交 |
| 状态行 `pending=true` 很久 | 查看是否能访问 Gitea；网络恢复后自动补推 |
| 推送被拒：考试已结束 | 正常，冻结后的推送会被服务器拒绝 |

## 检查点降级

agent 无法常驻时（例如机器策略限制），先确认工作区已初始化（`exam-agent -init`，或本地历史与远端一致），
然后用同目录的配置文件执行单次检查点：

```bash
exam-agent -config .exam-agent.json -once
```

它做一次“提交白名单内改动 + 推送”后退出，可放到手动快捷方式或每 20 分钟的计划任务里。
服务器端白名单、冻结与 final commit 逻辑与常驻模式完全相同。
