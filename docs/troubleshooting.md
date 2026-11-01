# 踩坑与已知问题台账

记录联调/压测中实际遇到的问题、处理方式和遗留边界。新问题按同样格式追加，不要删除历史条目。

## 已修复（有实测依据）

| 编号 | 问题 | 现象 | 处理 |
|---|---|---|---|
| K-01 | Gitea 容器无法回调 examd | Gitea 日志 `webhook can only call allowed HTTP servers ... deny '127.0.0.1'`，看板 push_count 始终为 0 | compose 增加 `GITEA__webhook__ALLOWED_HOST_LIST=*`；手工部署在 `app.ini` 加 `[webhook] ALLOWED_HOST_LIST = *`，文档已注明 |
| K-02 | 容器内 webhook 地址不能用考生访问地址 | examd 在容器/宿主分离时，`public_url` 给考生用，Gitea 容器访问不到 | 新增 `gitea.webhook_url` 覆盖项；compose 默认 `http://examd:8080/api/hooks/gitea` |
| K-03 | 冻结时的 final commit 可能落后 | webhook 异步投递，手动冻结紧接推送时，`repo.final_commit` 还是上一次提交 | 冻结改为读 Gitea `main` 分支头作为权威 final commit；读不到时回退到最近一次 push_event |
| K-04 | pre-receive 记录推送产生“幽灵提交” | 并发推送 ref 更新失败，但 pre-receive 已写 push_event，final commit 指向仓库里不存在的对象，判题报 `object not found` | pre-receive 只做放行/拒绝，不再写 push_event；推送记录统一由 post 阶段的 Gitea webhook 写入 |
| K-05 | 重复开通时令牌回退失败（Gitea 1.22） | 改密接口返回 `[LoginName]: Required`，随后 basic auth 建令牌 401 | `SetPassword` 请求补 `login_name`、`source_id`；实测可改密并用新密码建令牌 |
| K-06 | 重复开通/重置令牌 400 | `access token name has been used already`（令牌名固定为 `exam-agent`） | 每次签发使用唯一名称 `exam-agent-<纳秒时间戳>` |
| K-07 | 70 人 Docker 判题过慢 | 每个编译容器都从零编译标准库 | 判题编译容器挂载持久 `GOCACHE`/`GOPATH`（`judge.work_dir/gocache`、`gopath`），70 人全量判题 102.6 秒 |
| K-08 | 无法确认判题是否真的走了 Docker | 本机同时装了 Go，`local` 与 `docker` 都可能成功 | 压测配置把 `judge.go_bin` 指向不存在的路径，`runtime: docker` 仍 2/2 通过，证明容器路径生效 |
| K-09 | Windows 上演练脚本用错 bash | `push-load.sh` 被 WSL 的 `/bin/bash` 执行，报 `No such file or directory` | 用 Git for Windows 的 `bin\bash.exe` 执行；`deploy/drill/README.md` 已注明 |
| K-10 | Docker Desktop 看似“坏了” | `docker info` 只有 Client，没有 Server | 实际是 `com.docker.service` 与 WSL 发行版 `docker-desktop` 都停了；`docker desktop start` 后恢复，不是安装损坏 |
| K-11 | examd 镜像在国内网络构建失败 | `go mod download` 访问 `proxy.golang.org` 被拒（connection refused） | Dockerfile 增加 `GOPROXY` build-arg，默认 `https://goproxy.cn,direct`；实测镜像构建成功、容器内 `examd version` 正常 |
| K-12 | 心跳空提交污染复核 | 默认 10-25 秒一个空提交，2 小时约 240-480 条，`git log` 难以阅读 | 心跳默认改为 55-65 秒（仍满足每分钟有提交）；新增 `examd review`，默认过滤心跳并输出每次增删行、`bulk_change`/`long_gap` 标记 |
| K-13 | 心跳把真实改动包进 heartbeat | 改动未到防抖时间时，心跳先提交并带上这些改动；复核按消息过滤心跳后，真实改动会消失 | 心跳改为只在工作区没有未提交改动时创建纯空提交；`TestHeartbeatDoesNotSwallowChanges` 覆盖 |
| K-14 | 推送行数一直为 0 | webhook 不带增删行；pre-receive 记录会产生幽灵提交 | 增加 post-receive 钩子上报 `git diff` 统计；push_event 按 `commit_sha` 去重，webhook 与钩子任意先后到达都只保留一条并补全行数 |

## 2026-11 全量审查修复

| 编号 | 问题 | 修复 |
|---|---|---|
| P0-1 | compose 里 examd 容器判题时 `-v` 用了容器内路径，Docker 在宿主建空目录，全员编译失败 | 新增 `judge.host_work_dir`（容器内 work_dir 对应的宿主路径），docker 挂载统一映射；examd 在容器内且未配置时直接报错 |
| P0-2 | Gitea custom 目录/钩子路径不一致，安装脚本会中止或推送全被拒 | compose 固定 `GITEA_CUSTOM=/data/gitea/custom` 并只挂载 custom 子目录；`install-hooks.sh` 默认 `dist/` 二进制并增加自检 |
| P1-1 | 并发判题互相删除工作目录 | SQLite `judge_lock` + 每次运行独立 `repos/<runID>/`，HTTP/CLI 共用锁 |
| P1-2 | 学生程序无限输出打爆 examd 内存 | 子进程输出限制 1MB，超限终止并记 `output_limit`；判题容器加 pids/cap/read-only/tmpfs 限制 |
| P1-3 | 冻结先读分支头后改状态，窗口内推送可被接受 | 进入冻结先置 `frozen`，再记录 final commit、归档 |
| P1-4 | 判题失败保留旧成绩，报表与 CSV 不一致 | 失败也整批替换结果（`problem_id=0` 占位） |
| P1-5 | 重导题目后 problem id 变化，旧成绩错位 | 按 `(exam_id,key)` UPDATE，保留 problem id |
| P1-6 | 历史分叉时 agent 永久 non-fast-forward | 自动备份本地 main 到 `agent-backup-<时间>`，重置到远端 main 后重提并推送 |
| P1-7 | 签名脚本 `$args.TimestampServer` 抛异常 | 改为 `$signArgs.TimestampServer` |
| P1-8 | 漏配 judge 段默认 local，学生代码裸跑 | 默认 `docker`；`runtime: local` 必须显式 `judge.allow_local: true` |
| P2 | 钩子 token 为空、webhook 非 main/零 SHA、提交失败清 dirty、长任务绑定请求 ctx、备份混入 live db、时区解析、CSV BOM、题目内存限制、构建上下文泄密、CSRF、事件时间/配额、文档偏差 | 已逐项修复：见对应代码与测试 |

## 已知边界（本轮已关闭）

| 编号 | 事项 | 处理 | 验证 |
|---|---|---|---|
| L-01 | agent 只检测网卡/IP | 增加无线 SSID 探测（Windows `netsh`、macOS `airport`/`networksetup`、Linux `iwgetid`/`nmcli`），30 秒缓存，随网络指纹写入 `network_change` | `internal/agent/ssid_test.go` 覆盖三种输出解析 |
| L-02 | `agent_event` 只落本地 | 新增 `POST /api/agent/events`（考生令牌鉴权），写入 `agent_event` 表；看板增加“agent 事件”列；agent 配置 `examd_url` 后自动上报 | httpapi 测试校验事件落库与错误令牌 401 |
| L-03 | 登录无限流 | 考生/管理端登录按 IP 做令牌桶限流（10 次/分钟），超限返回 429 | `internal/httpapi/limiter_test.go` |
| L-04 | 自签名 HTTPS 无 CA 配置 | 新增 `gitea.ca_file`，同时用于 Gitea API、模板推送、判题 clone；agent 增加 `ca_file` / `insecure_skip_verify` | Gitea 客户端坏 CA 报错测试；go-git 使用 `CABundle`/`InsecureSkipTLS`；自签名证书全链路未联调 |
| L-05 | 手动冻结可能落后一次推送 | 冻结以 Gitea `main` 分支头为准，读不到才回退 push_event | 70 人压测中 final_commit 与分支头一致、缺失 0 |
| L-06 | 检查点模式依赖手动 git | `exam-agent -once` 执行一次提交+推送，不需要本机 git | `TestRunOnceCheckpoint` 对 bare 仓库实测 |
| L-07 | 70 台真机未演练 | 按约定用 70 客户端压测关闭软件侧；真机现场演练仍列入运维清单 | 70/70 开通、70/70 推送、Docker 判题 70 人 |
| L-08 | 目录未初始化 git | 已 `git init` 并创建首个提交，后续按正常流程提交 | `git log --oneline` 可见初始提交 |

## 仍需现场执行

| 事项 | 说明 |
|---|---|
| 70 台真机 + AP + DHCP | 软件端已用 70 并发客户端验证；真机环境按 `deploy/drill/README.md` 在 T-7 执行 |
| 真实断电 | 已做“强杀 examd + 备份恢复”演练；市电断电需现场配合 |
| 真实拔网线 | 已用停 Gitea 模拟连接拒绝与恢复补推；物理断网可在现场复跑同一流程 |

## 复现记录

- 70 客户端压测：开通 39.8s、并发推送 18.3s、看板 70/70、冻结 final commit 缺失 0、Docker 判题 102.6s。
- 断网演练：停 Gitea 20s，agent 出现 `push failed`（2s/4s/8s/16s 退避），Gitea 恢复后自动补推。
- 崩溃恢复：强杀 examd 后重启数据正常；备份包恢复到全新目录后报表逐行一致。
- 事件上报：常驻 agent 跑 10 秒后看板 `push_count=3`、`last_agent_type=start`。
- 检查点：`exam-agent -once` 输出 `checkpoint pushed`，push_count 从 3 变为 4。
- 登录限流：连续 12 次错误令牌，前 10 次 401，第 11/12 次 429。
- 复核时间线：仓库共 5 个提交（3 个心跳），`examd review` 默认输出 2 行；一次插入 300 行的提交标记为 `bulk_change`。
