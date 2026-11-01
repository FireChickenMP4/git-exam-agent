# exam-agent 使用说明

考生本机的自动提交与推送程序。它只用 Go 实现 git 操作（go-git），考场机器不需要安装 Git。

## 行为

1. 在 `workspace` 初始化仓库（默认分支 `main`）并做首次提交。
2. 递归监听工作区变化，安静 3 秒后提交（`debounce_ms`）。
3. 提交后随机等待 10-30 秒再推送（`push_interval_ms_min/max`），把多次提交合并成一次 push。
4. 无改动时按 `heartbeat_ms_min/max`（默认 10-25 秒）创建空提交，证明 agent 仍在运行。
5. 推送失败进入指数退避：2s、4s、8s……上限 60s，恢复后自动补推。
6. 每 5 秒检查网卡地址变化；无线环境每 30 秒读取一次 SSID，变化时写入 `network_change` 事件。
7. 每 10 秒输出状态行（最后提交、最后推送、失败次数、网络）。
8. 退出（Ctrl+C / 关机）前尽力提交并推送一次。
9. 配置了 `examd_url` 时，事件同时上报 examd，看板显示最后一条 agent 事件。

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
  "debounce_ms": 3000,
  "push_interval_ms_min": 10000,
  "push_interval_ms_max": 30000,
  "heartbeat_ms_min": 10000,
  "heartbeat_ms_max": 25000
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

## 常见问题

| 现象 | 处理 |
|---|---|
| `push failed ... 401` | 令牌错误或已重置，使用 `examd token` 重新打印 |
| `push failed ... connection refused` | 网络未连上考场 AP；agent 会自动重试 |
| `skipped N non-Go files` | 正常提示，非 Go 文件不会提交 |
| 状态行 `pending=true` 很久 | 查看是否能访问 Gitea；网络恢复后自动补推 |
| 推送被拒：考试已结束 | 正常，冻结后的推送会被服务器拒绝 |

## 检查点降级

agent 无法常驻时（例如机器策略限制），用同目录的配置文件执行单次检查点：

```bash
exam-agent -config .exam-agent.json -once
```

它做一次“提交白名单内改动 + 推送”后退出，可放到手动快捷方式或每 20 分钟的计划任务里。
服务器端白名单、冻结与 final commit 逻辑与常驻模式完全相同。
