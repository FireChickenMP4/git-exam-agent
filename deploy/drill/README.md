# 演练脚本

| 演练 | 做法 |
|---|---|
| 10 人模拟 | 开通后执行 `push-load.sh slips.csv 10 1`，看板确认 10 人都有推送 |
| 70 台真机 / 70 客户端压测 | 无真机时用 `push-load.sh slips.csv 70 1` 模拟 70 个并发客户端；考场 T-7 再用 70 台真机执行同样脚本 |
| 冻结演练 | 场次 `ends_at` 设为 2 分钟后，开考后等到点；push 应返回“考试已结束”，看板 final commit 非空 |
| 断网演练 | 考生机上拔网线 1 分钟再恢复，agent 状态行 `fails` 上升、恢复后自动补推 |
| 断电恢复 | 考试中途 `docker compose stop`，用 `examd backup` 的包在干净机器 `examd restore` 后继续 |
| 令牌重置 | `examd token -exam ... -student ...`，考生更新 `.exam-agent.json` 后重启 agent |

`push-load.sh` 需要 `slips.csv`（`examd provision -slips` 生成），脚本会用令牌拼出带凭据的 clone URL。

Windows 上请用 Git for Windows 的 bash 执行，避免落到 WSL 的 `/bin/bash`：

```powershell
& "C:\Program Files\Git\bin\bash.exe" deploy/drill/push-load.sh slips.csv 10 1
```
