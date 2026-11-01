# 考场运维手册

> 联调踩坑、已修复问题和遗留边界见 `docs/troubleshooting.md`。

## 0. 组件

| 组件 | 位置 |
|---|---|
| Gitea | 考场服务器 :3000 |
| examd | 考场服务器 :8080（考生页、管理页、钩子、报表、判题 CLI） |
| exam-agent | 考生笔记本，登录自启 |
| exam-hook | Gitea 容器内 pre-receive 钩子 |

`public_url` 必须是考生能访问的地址（例如 `http://10.0.0.10:8080`），它用于 Gitea webhook 与页面链接。

examd 在 compose 内运行时，`gitea.url` 仍填宿主 IP（例如 `http://10.0.0.10:3000`）：Gitea API 返回的
`clone_url` 会写进凭条并用于判题 clone，写成服务名 `http://gitea:3000` 考生侧不可达。

## 1. 部署

```bash
cp deploy/.env.example deploy/.env            # 改口令
cp deploy/config-example.yaml deploy/config.yaml
docker compose -f deploy/docker-compose.example.yml up -d --build
bash deploy/gitea/init.sh                       # 创建管理员并打印 GITEA_ADMIN_TOKEN
```

> examd 镜像构建默认使用 `GOPROXY=https://goproxy.cn,direct`；海外构建机可加
> `--build-arg GOPROXY=https://proxy.golang.org,direct`。

把 `init.sh` 打印的令牌写入 `deploy/.env` 的 `GITEA_ADMIN_TOKEN`，重启 examd：

```bash
docker compose -f deploy/docker-compose.example.yml up -d examd
```

> 推送记录依赖 Gitea webhook。Gitea 默认只允许 webhook 回调白名单内的地址，compose 示例已设置
> `GITEA__webhook__ALLOWED_HOST_LIST=*`；手工部署时需要在 `app.ini` 增加
> `[webhook] ALLOWED_HOST_LIST = *`（或填入 examd 的地址），否则 webhook 会被 Gitea 拒绝，
> 看板与 final commit 只能靠判题时的 clone 兜底。
>
> Gitea 在容器里、examd 在宿主或另一个容器时，把 `gitea.webhook_url` 设成 Gitea 能访问的
> examd 地址（例如 `http://examd:8080/api/hooks/gitea`）；它和考生访问的 `public_url` 是两回事。

安装 pre-receive 钩子（编译 linux 二进制后）：

```bash
make dist
bash deploy/gitea/install-hooks.sh bin/exam-hook-linux-amd64
```

脚本会同时安装 `pre-receive`（拦截）和 `post-receive`（上报推送文件数与增删行）；两者共用同一个
`exam-hook` 二进制。push_event 按 commit 去重，webhook 与 post-receive 任意先后到达都不会重复。

判题镜像：

```bash
make judge-image
```

> 不使用 Docker 判题时，把 `judge.runtime` 改为 `local`，并保证服务器装有 Go 1.26。
> `runtime: docker` 要求 examd 所在环境能访问 Docker daemon；examd 跑在容器里时按 compose 示例挂载
> `/var/run/docker.sock`。判题容器无网络，只挂考生代码与只读输出目录。

## 2. 赛前准备

```bash
BIN=bin/examd
CFG=deploy/config.yaml

# 管理员（首次）
$BIN admin -config $CFG -username admin -password '强口令'

# 场次
$BIN exam -config $CFG -id 2027-spring -title "2027 春季招新机试" \
  -starts-at "2027-02-15T14:00:00+08:00" -ends-at "2027-02-15T16:00:00+08:00"

# 名单（student_no,name,track,variant）
$BIN roster -config $CFG -exam 2027-spring -csv roster.csv

# 题目目录（格式见 docs/problems.md）
$BIN problem -config $CFG -exam 2027-spring -dir problems/

# 开通用户/仓库/令牌/webhook/分支保护，并打印凭条
$BIN provision -config $CFG -exam 2027-spring -slips slips.csv
```

`slips.csv` 含每人一次性令牌，打印后按人分发；令牌丢失用：

```bash
$BIN token -config $CFG -exam 2027-spring -student 20231234
```

练习仓库：在 Gitea 里建一个普通仓库即可，`allow_unknown_repos: true` 时不受冻结限制。

## 3. 考试期间

- 考生页：`http://10.0.0.10:8080/`
- 管理页：`http://10.0.0.10:8080/admin`
- 看板每 5 秒刷新：最后推送超过 2 分钟标黄，超过 3 分钟标红。
- 公告在管理页发布，考生页一分钟内刷新。
- 每 30 分钟备份一次（管理页“备份”或 `examd backup`）。

## 4. 到点冻结

`exam.auto_freeze: true` 时，`ends_at + freeze_grace_seconds`（默认 30 秒）后自动：

1. 以 Gitea 上 `main` 的实际分支头记录 `repo.final_commit`（webhook 未及时投递也不会漏），
   读取失败的考生回退为最后一次推送事件；
2. 场次状态改为 `frozen`；
3. 归档 Gitea 仓库（只读）；
4. pre-receive 钩子开始拒绝推送。

也可以管理页手动“冻结”。

## 5. 考后

```bash
# 报表
$BIN export -config $CFG -exam 2027-spring -out report.csv

# 批量判题（默认 judge.runtime）
$BIN judge -config $CFG -exam 2027-spring -out results.csv
$BIN judge -config $CFG -exam 2027-spring -out one.csv -student 20231234

# 复核时间线（默认排除心跳提交）
$BIN review -config $CFG -exam 2027-spring -out review.csv
$BIN review -config $CFG -exam 2027-spring -student 20231234 -out one.csv
$BIN review -config $CFG -exam 2027-spring -include-heartbeat -out review-all.csv

# 备份 / 恢复（恢复前停止 examd）
$BIN backup -config $CFG -out backup.tar.gz
$BIN restore -config $CFG -in backup.tar.gz -force
```

判题结果同时写入 `judge_result` 表，报表中的 `judge_score` 与每题 `passed/total` 来自这里。

### 复核时间线字段

`examd review` 逐 commit 计算与父提交的差异，输出：

| 字段 | 含义 |
|---|---|
| `time` / `gap_seconds` | commit 时间（客户端时钟）与距上一提交的间隔 |
| `kind` | `initial` / `template` / `auto` / `checkpoint` / `heartbeat` / `empty` / `other` |
| `files` / `insertions` / `deletions` | 本次提交改动的文件数与增删行 |
| `flags` | `bulk_change`（单次 >=200 行或 >=15 个文件）、`long_gap`（>10m）、`before_window` / `after_window` |

注意：

- 默认不输出心跳提交，需要时加 `-include-heartbeat`。
- commit 时间是考生机器时钟，可能被修改；服务器收到推送的时间以 `push_event` 为准。
- `bulk_change` 只是“大块改动”的启发式标记，不能证明是粘贴；系统不采集键盘/粘贴事件。
- agent 的 `bulk_edit` 事件（保存级、默认 >4KB）是同一类启发式信号，只能说明“这一次保存写入了很多字节”。
- 复核需要 clone 仓库，使用与判题相同的 Gitea 凭据和 `ca_file` 配置。

## 6. 验收对照

| 编号 | 验收项 | 操作 |
|---|---|---|
| V-01 | 名单导入 | `examd roster`，错误行单独报告 |
| V-02 | 批量开通 | `examd provision` / 管理页“批量开通” |
| V-03 | 一人一仓 | Gitea 用户只能访问自己的仓库 |
| V-04 | 题面隔离 | 考生只拿到 `variant` 对应的题面 |
| V-05 | 水印 | 考生页全屏水印（学号+姓名+时间） |
| V-06 | agent 提交 | 改一行后 3 秒 commit、30 秒内 push |
| V-07 | 心跳 | 无改动时每 10-25 秒空提交 |
| V-08 | 断档告警 | 停 agent 2 分钟看板标黄，3 分钟标红 |
| V-09 | 冻结 | 到点后 push 返回“考试已结束” |
| V-10 | force-push | Gitea 分支保护 + 钩子双重拒绝 |
| V-11 | 路径白名单 | 钩子拒绝 `.png`、`README.md` |
| V-12 | 大文件 | 6MB 文件被拒 |
| V-13 | webhook 签名 | 错误签名 401 |
| V-14 | final commit | 判题时 checkout 并校验 HEAD |
| V-15 | 批量判题 | `examd judge`，并发 2/8 |
| V-16 | 备份恢复 | `examd backup` + `examd restore` |
| V-17 | 70 台推送 | `deploy/drill/push-load.sh` |
