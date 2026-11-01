# Git + Agent 方案实施计划

> 版本：v1.0
> 日期：2026-11-01
> 状态：实施中
> 合并版参考：`../3.md`

---

## 1. 目标

在无外网的考场内，完成一场 70 人、90-120 分钟的 Go 机试：

- 考生本机编译、运行
- 代码通过 Git 推送到考场服务器
- 服务器只做 Git 托管、推送记录、题面、报表
- 判题考后批量执行

不提供浏览器 IDE；不依赖公网；不做硬性防作弊。

---

## 2. 范围

### 2.1 v1 包含

- 场次、名单、赛道
- 一人一用户、一人一私有仓库
- 单考生令牌生成与打印
- 静态题面页：令牌登录、水印、按人/按批变体
- 仓库模板：`go.mod`、`main.go`、样例、本地运行脚本
- `exam-agent`：自动提交、推送、心跳、离线补推
- 推送事件采集：Gitea webhook + 考后 clone 校验
- 冻结：到点拒绝推送，记录 final commit
- 批量判题：clone、`go build`、隐藏用例、CSV
- 监考看板：最后推送、断档、agent 状态
- 报表、备份、恢复
- 管理端鉴权与审计

### 2.2 v1 不包含

- 浏览器 IDE、Monaco、code-server
- 在线编辑、在线样例运行
- 编辑事件流、回放
- ICPC 规则、实时榜单
- AI 面板、屏幕录制
- 多语言判题
- 多租户

---

## 3. 约束

| 编号 | 约束 |
|---|---|
| C1 | 考场局域网自带 AP，WAN 不接 |
| C2 | 考生自带笔记本，需有 Go 1.26 或兼容版本 |
| C3 | 70 人，监考 5-10 人 |
| C4 | 服务器用 Mac Studio 或任意 8GB+ 机器；云服务器只做镜像/备份 |
| C5 | 考试期间无外网 |
| C6 | 换届交接，单人可部署 |
| C7 | Go + TypeScript |
| C8 | 90-120 分钟，4-6 题 |
| C9 | 一人一仓 |
| C10 | 判题 Go 1.26；stdlib 或 vendor |
| C11 | 题面按"会泄露"设计 |

---

## 4. 拓扑

```
考生笔记本
  编辑器 + Go + exam-agent
        │ git push (HTTPS)
        ▼
AP（无外网）── 路由器 ── 服务器
                          ├─ Gitea :3000
                          ├─ examd :8080
                          ├─ SQLite
                          └─ 判题（可选）
                          │
                    考后镜像 → 自有服务器链
```

- 路由器只做 DHCP 与有线；WAN 不插。
- 2 台 AP，每台 35 客户端以内；5GHz；客户端隔离。
- 服务器固定 IP，例如 `10.0.0.10`。
- 题目页 `http://10.0.0.10:8080`；Git `http://10.0.0.10:3000`。

---

## 5. 组件

| 组件 | 作用 |
|---|---|
| Gitea | 用户、仓库、令牌、webhook、分支保护 |
| examd | 名单、开通、题面、看板、报表、判题 CLI |
| SQLite | 场次、考生、推送事件、判题结果 |
| exam-agent | 考生本机自动提交与推送 |
| 判题镜像 | `exam-go:1.26`，Docker |
| 静态页 | 题面、公告、状态 |

---

## 6. 流程

| 时间 | 事项 |
|---|---|
| T-14 | 名单、题目、变体、仓库模板、agent、离线 Go |
| T-7 | 70 台真机 push 演练；冻结演练；备份恢复 |
| T-1 | 场地、AP、服务器、Gitea 启动；核对 IP |
| T-30 分钟 | 建场次；批量开通用户/仓库/令牌；检查 agent |
| T-10 分钟 | 练习仓库：clone、改一行、push |
| T0 | 开考 |
| 考试期间 | 看板；每 30 分钟备份 |
| T0 到点 | 30 秒宽限后归档仓库；记录 final commit |
| 冻结后 | 导出报表；批量判题 |
| T+1 至 T+3 | 复核 |

---

## 7. 仓库与 agent

### 7.1 一人一仓

- 一个考生一个 Gitea 用户，一个私有仓库 `exam-2027-<学号>`。
- 考生是仓库 owner；管理员是站点管理员。
- 令牌只对该仓库有写权限。
- `main` 分支保护：禁止 force-push、禁止删除。

### 7.2 仓库模板

```
exam-2027/
├── go.mod              # module exam; go 1.26
├── main.go
├── README.md
├── testdata/
├── run.sh
├── run.ps1
└── .gitignore
```

### 7.3 exam-agent

- Go 二进制；`fsnotify` + `go-git`；不要求本机装 Git。
- 配置 `.exam-agent.json`：

```json
{
  "repo": "https://exam.local:3000/20231234/exam-2027-20231234.git",
  "username": "20231234",
  "token": "<token>",
  "workspace": ".",
  "debounce_ms": 3000,
  "push_interval_ms_min": 10000,
  "push_interval_ms_max": 30000,
  "heartbeat_ms_min": 10000,
  "heartbeat_ms_max": 25000
}
```

行为：

1. 初始化仓库，首次提交
2. 监听变化，防抖 3 秒
3. `add` / `commit` / `push`
4. 心跳：随机 10-25 秒，空提交或 `.heartbeat`
5. 推送失败：本地排队，指数退避
6. 检测网络接口/SSID 变化，写 `agent_event`
7. 状态行：最后提交、最后推送、失败次数、当前网络

安装：

- Windows：用户级计划任务，登录自启，异常重启
- macOS：LaunchAgent
- Linux：systemd user unit
- 不需要管理员权限

### 7.4 检查点模式（降级）

- 不运行 agent；仓库内提供 `push.ps1` / `push.sh`。
- 考点在 T-60/T-40/T-20/T-5 广播；考生双击提交。
- 看板显示最后推送；未推送标红。
- 与 agent 二选一；默认 agent。

### 7.5 服务端钩子

`pre-receive`：

1. 读取旧值、新值、ref
2. 冻结后（`ends_at + 30 秒`）拒绝
3. 只允许 `refs/heads/main`；拒绝删除与 force-push
4. `git ls-tree -r -l <new>`：
   - 只允许 `*.go`
   - 单文件 > 5MB 拒绝
   - 单次新增 > 20MB 拒绝
5. 仓库 > 200MB 拒绝
6. 输出中文原因

Webhook：

- Gitea push webhook -> `POST /api/hooks/gitea`
- 校验 `X-Gitea-Signature`（HMAC-SHA256）
- 写 `push_event`；失败不影响 push，考后 clone 兜底

---

## 8. 题面与答题约束

- 令牌登录；只返回本人变体。
- `Cache-Control: no-store`。
- 水印：学号 + 时间。
- 禁止选择、右键、复制、打印。
- `visibilitychange` 遮罩。
- 不做 canvas/图片渲染；不把页面当安全边界。

答题约束：

- 原创题、绑环境、多步、有取舍。
- 按批次或学号发不同变体。
- 通过者现场 10-15 分钟改一个约束。
- 评分 = 代码 + 现场 + 过程记录。
- 收手机、一台设备、断网。

---

## 9. 判题

默认考后批量：

```
examd judge --exam 2027-spring --out results.csv
```

1. 读取 `candidate` 与 `repo.final_commit`
2. clone -> checkout -> 校验 HEAD
3. 编译容器：`go build -o /out/main .`
4. 运行容器：逐用例；宿主比对
5. 写 `judge_result`；汇总 CSV

环境：

- `exam-go:1.26`，digest 固定
- `CGO_ENABLED=0`、`GOPROXY=off`、stdlib 或 vendor
- 编译 1GB / 10 秒 / 并发 2
- 运行 256MB / 3 秒 / 并发 8
- 期望输出在宿主；workdir 只含考生代码

可选推送触发：

- Gitea webhook -> 队列 -> 判题 -> 状态页
- 每人每题 20 次；队列 200

---

## 10. 数据模型

```
admin(id, username, password_hash, role, disabled, created_at)
admin_session(id, admin_id, token_hash, created_at, expires_at, ip, ua)

exam(id, title, type, status, starts_at, ends_at, timezone, created_at)
track(id, exam_id, name, sort)
problem(id, key, title, statement_md, score, time_limit_ms, memory_limit_mb, created_at)
variant(id, problem_id, seed, statement_md, assets_dir)
test_case(id, problem_id, variant_id, case_no, input_path, expected_path, score, is_sample, sort)

candidate(id, exam_id, track_id, student_no, name, gitea_user, repo_name,
          token_hash, variant_id, status, created_at)
candidate_session(id, candidate_id, token_hash, created_at, expires_at, ip, ua)
consent(id, candidate_id, version, accepted_at, ip)

repo(id, candidate_id, url, default_branch, final_commit, last_push_at, archived)
push_event(id, candidate_id, ref, commit_sha, received_at,
           lines_added, lines_deleted, files_changed, source)
agent_event(id, candidate_id, t, type, payload_json)

judge_result(id, candidate_id, problem_id, variant_id, judged_at,
             status, passed, total, score, detail_json)

notice(id, exam_id, t, content, author_id)
audit(id, exam_id, actor_type, actor_id, action, target, t, detail)
```

---

## 11. 接口

### 考生页

| 方法 | 路径 |
|---|---|
| POST | /api/login |
| GET | /api/me |
| GET | /api/problems |
| GET | /api/problems/{id} |
| GET | /api/status |
| GET | /api/notice |
| GET/POST | /api/consent |

### 管理端

| 方法 | 路径 |
|---|---|
| POST | /api/admin/auth/login |
| POST | /api/admin/exams |
| POST | /api/admin/exams/{id}/roster |
| POST | /api/admin/exams/{id}/provision |
| POST | /api/admin/exams/{id}/start |
| POST | /api/admin/exams/{id}/freeze |
| POST | /api/admin/exams/{id}/close |
| GET | /api/admin/exams/{id}/live |
| GET | /api/admin/exams/{id}/export.csv |
| POST | /api/admin/exams/{id}/judge |
| POST | /api/admin/exams/{id}/backup |

### 钩子

| 方法 | 路径 |
|---|---|
| POST | /api/hooks/gitea |
| POST | /api/hooks/pre-receive |

### Gitea Admin API

| 用途 | 接口 |
|---|---|
| 创建用户 | `POST /api/v1/admin/users` |
| 创建私有仓库 | `POST /api/v1/admin/users/{username}/repos` |
| 创建令牌 | `POST /api/v1/users/{username}/tokens` |
| 配置 webhook | `POST /api/v1/repos/{owner}/{repo}/hooks` |
| 归档仓库 | `PATCH /api/v1/repos/{owner}/{repo}` |
| 分支保护 | `POST /api/v1/repos/{owner}/{repo}/branch_protections` |

---

## 12. 验收

| 编号 | 验收项 | 方法 |
|---|---|---|
| V-01 | 名单导入 | 70 行，含 2 行错误 |
| V-02 | 批量开通 | 70 用户/仓库/令牌 |
| V-03 | 一人一仓 | 推他人仓库 403 |
| V-04 | 题面隔离 | 只看到本人变体 |
| V-05 | 水印 | 截图可辨认 |
| V-06 | agent 提交 | 改一行 3 秒内 commit，30 秒内 push |
| V-07 | 心跳 | 无改动时每分钟有提交 |
| V-08 | 断档告警 | 停止 agent 2 分钟看板标红 |
| V-09 | 冻结 | T0+30 秒后 push 拒绝 |
| V-10 | force-push | 被拒绝 |
| V-11 | 路径白名单 | 改 README 或加 .png 被拒绝 |
| V-12 | 大文件 | 6MB 文件被拒绝 |
| V-13 | webhook 签名 | 错误签名 401 |
| V-14 | final commit | checkout HEAD 一致 |
| V-15 | 批量判题 | 70 份 30 分钟内完成 |
| V-16 | 备份恢复 | 干净机器恢复 |
| V-17 | 70 台推送 | 真机同时 push |

---

## 13. 里程碑

| 阶段 | 周次 | 交付 |
|---|---|---|
| M0 | W1-W2 | Gitea、名单、开通、题面页、agent |
| M1 | W3-W4 | 推送记录、看板、冻结、报表 |
| M2 | W5-W6 | 批量判题、备份恢复 |
| M3 | W7-W8 | 70 台真机、离线 Go、断电恢复 |
| 缓冲 | W9+ | 修缺陷、文档、交接 |

人力：2 人，每周 10-12 小时。目标可用：2027-02-15。

---

## 14. 风险

| 编号 | 风险 | 缓解 |
|---|---|---|
| R-01 | agent 被停止 | 服务自启；心跳；看板 |
| R-02 | 笔记本休眠 | 考前要求关闭休眠 |
| R-03 | 考生无 Go | 离线安装包；备用机 |
| R-04 | 推送失败 | agent 排队重试 |
| R-05 | 截图泄露 | 水印；变体；收手机 |
| R-06 | force-push | 分支保护 |
| R-07 | 令牌泄露 | 只对该仓库有写权限；可重置 |
| R-08 | Gitea 单点 | 备份；备用机 |
| R-09 | 时钟偏差 | 考前校时；服务端时间 |
| R-10 | 判题环境差异 | 固定 Go 1.26；stdlib/vendor |
| R-11 | 大文件 | 钩子限制 |
| R-12 | 一人多号 | 令牌绑定学号 |

---

## 15. 开放问题

| 编号 | 问题 | 默认 |
|---|---|---|
| Q-01 | Gitea 还是裸仓库 | Gitea |
| Q-02 | agent 还是检查点模式 | agent |
| Q-03 | 变体按批次还是按学号 | 按学号 |
| Q-04 | 判题批量还是推送触发 | 批量 |
| Q-05 | 是否允许下载/打印题面 | 不允许 |
| Q-06 | 是否对外借出 | 待定 |

---

## 16. 下一步（M0）

1. 建仓库骨架：`cmd/examd`、`cmd/exam-agent`、`internal/`、`web/`、`deploy/`
2. `go.mod`、`Makefile`、CI（gofmt/vet/test）
3. Compose：Gitea + examd
4. 名单导入 + Gitea 批量开通
5. 仓库模板
6. agent 主循环：fsnotify + go-git + 心跳 + 重试
7. 题面页：令牌登录 + 水印
8. `pre-receive` 钩子
9. 报表 CSV
10. 10 人模拟
