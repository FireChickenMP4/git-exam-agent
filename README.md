# Git + Agent 方案

> 状态：M0 完成
> 目标场次：2027 春季招新机试
> 详细计划：`PLAN.md`
> 任务清单：`TODO.md`

## 一句话

考生用自己熟悉的编辑器和 Go 工具链，本地编译；`exam-agent` 自动提交并推送到考场局域网的 Gitea；服务器只做 Git 远端、推送记录、题面和报表；判题考后批量做。

## 为什么是这条

- 编辑器是考生自己的 VS Code / GoLand，不需要浏览器 IDE。
- 服务器没有每用户 Node 进程，8GB 机器就能跑。
- 代码通过 Git 留存，推送时间以服务器为准。
- 不依赖公网；考场 AP 不接 WAN。

## 关键决策

| 项 | 决定 |
|---|---|
| 仓库 | 一人一用户、一人一私有仓库 |
| 提交 | `exam-agent` 自动提交；检查点模式作为降级 |
| 强制 | 不做硬性强制；agent 停了会有断档，进人工复核 |
| 判题 | 默认考后批量；推送触发为可选项 |
| 语言 | 仅 Go 1.26；只允许标准库或仓库内 `vendor/` |
| 题面 | 按"会泄露"设计；水印 + 变体；页面不是安全边界 |
| 网络 | 路由器 + 2 台 AP；WAN 不接；服务器固定 IP |
| 服务端 | Gitea + examd + SQLite；不引入 Nacos |

## 当前进度

M0 已完成，任务清单见 `TODO.md`。仍需现场执行的部分（70 台真机推送、真实断网演练）已准备好脚本与步骤，见 `deploy/drill/README.md`。

## 工程结构

```text
cmd/examd/          examd：HTTP 服务 + 管理/开通/判题/备份 CLI
cmd/exam-agent/     考生本机自动提交与推送
cmd/exam-hook/      Gitea pre-receive 钩子
internal/agent      agent 主循环（fsnotify + go-git）
internal/store      SQLite 数据层
internal/gitea      Gitea Admin API 客户端
internal/provision  批量开通用户/仓库/令牌/webhook/分支保护
internal/gate       推送白名单与大小限制
internal/judge      批量判题
internal/httpapi    考生端/管理端 API 与 webhook
internal/roster     名单 CSV
internal/problem    题目导入
internal/backup     备份与恢复
web/                考生页与管理页（go:embed 内嵌）
deploy/             compose、钩子、agent 安装脚本、演练脚本
docs/               使用说明
```

## 快速启动

```bash
make build                 # 生成 bin/examd、bin/exam-agent、bin/exam-hook（Windows 下为 .exe）
make test                  # 单元 + 集成测试
make dist                  # 三平台构建

cp deploy/config-example.yaml deploy/config.yaml
# 按考场地址修改 listen/public_url/gitea.*；本机试跑可先设 judge.runtime: local

bin/examd admin -config deploy/config.yaml -username admin -password 'admin-pass'
bin/examd exam  -config deploy/config.yaml -id 2027-spring -title "2027 春季招新机试" \
  -starts-at "2027-02-15T14:00:00+08:00" -ends-at "2027-02-15T16:00:00+08:00"
bin/examd serve -config deploy/config.yaml
```

- 考生页：`http://127.0.0.1:8080/`
- 管理页：`http://127.0.0.1:8080/admin`

完整部署、开通、冻结、判题、备份恢复步骤见 `docs/operations.md`；
agent 配置与安装见 `docs/agent.md`；题目目录格式见 `docs/problems.md`；
联调踩坑与已知问题见 `docs/troubleshooting.md`。

## 已实现（M0）

- 工程骨架、Makefile、CI、三平台构建
- 一人一用户一私有仓库，批量开通用户/仓库/令牌/webhook/分支保护，打印凭条
- 仓库模板（go.mod、main.go、.gitignore）
- exam-agent：防抖提交、随机间隔推送、心跳空提交、指数退避、网络变化事件、状态行
- 题面页：令牌登录、变体、水印、禁止复制/右键/打印、切屏遮罩、公告、须知确认
- pre-receive 钩子：分支/force-push/路径白名单/大小/冻结
- 管理端：名单导入、开通、开考、冻结、封存、看板、公告、报表、判题、复核时间线、备份
- 判题：clone/checkout/编译/逐用例/落库/CSV（docker 或 local runtime）
- 备份与恢复、自动冻结、审计记录

## 验证

- `go test ./...`：agent（含真实 bare 仓库 push）、store、gate、roster、problem、judge（local runtime 编译/运行）、
  httpapi（登录/变体/webhook 签名/冻结）、provision（mock Gitea + 模板推送）、backup、cli 全部通过。
- 本机 Gitea 1.22.6 端到端（10 人）：批量开通用户/仓库/令牌、模板推送、agent 自动提交与推送、
  pre-receive 拒绝非 Go 文件、10 人并发推送、冻结记录 final commit、冻结后推送被拒、
  批量判题（正确解法 2/2 满分）、备份与恢复。
- 70 客户端压测（模拟 70 台考生设备，Gitea 运行在 Docker）：开通 70 人 39.8 秒，
  70 人并发推送 18.3 秒，看板 70/70 有推送，冻结 70 人 final commit 齐全；
  `judge.runtime=docker` 全量判题 70 人 102.6 秒，正确解法 2/2、100 分。
  为证明走的是 Docker 而非本机 Go，压测配置把 `judge.go_bin` 故意指向不存在的路径。
- 断网演练：停掉 Gitea 20 秒，agent 出现 `push failed` 并按 2s/4s/8s/16s 退避，
  Gitea 恢复后自动补推成功。
- 复核：`examd review` 默认过滤心跳提交，输出每次提交的增删行与 `bulk_change` / `long_gap` 标记。
- `make lint`、`make build`、`make dist`（windows/darwin/linux）通过。
