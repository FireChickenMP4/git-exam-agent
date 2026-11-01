# M0 任务清单

## 仓库与工程

- [x] 建目录：`cmd/examd`、`cmd/exam-agent`、`internal/`、`web/`、`deploy/`、`docs/`
- [x] `go.mod`（Go 1.26）
- [x] `Makefile`：fmt、fmt-check、vet、test、tidy、lint、build
- [x] CI：gofmt、go vet、go test
- [x] `LICENSE`（MIT）
- [x] `README.md` 快速启动

## 部署

- [x] `docker-compose.example.yml`：Gitea + examd
- [x] `.env.example`
- [x] `config-example.yaml`
- [x] Gitea 初始化脚本：管理员、站点配置
- [x] 数据卷：Gitea、examd、题目、配置

## 名单与开通

- [x] CSV 导入：学号、姓名、赛道
- [x] Gitea 批量创建用户
- [x] 批量创建私有仓库 `exam-2027-<学号>`
- [x] 批量创建访问令牌
- [x] 分支保护 `main`
- [x] 输出打印凭条（学号、令牌、仓库地址）

## agent

- [x] 配置读取
- [x] `fsnotify` 监听 + 3 秒防抖
- [x] `go-git` init/add/commit/push
- [x] 心跳（10-25 秒随机）
- [x] 推送失败排队与指数退避
- [x] `agent_event`：start/stop/network_change/push_fail
- [x] 状态行输出
- [x] Windows 计划任务 / macOS LaunchAgent / Linux systemd 安装脚本
- [x] 三平台构建

## 题面页

- [x] 令牌登录、会话 Cookie
- [x] 按人/按批变体
- [x] 水印
- [x] `Cache-Control: no-store`
- [x] 禁止复制/右键/打印
- [x] `visibilitychange` 遮罩
- [x] 公告
- [x] 告知与确认

## 服务端

- [x] 推送 webhook 接收与签名校验
- [x] `pre-receive` 钩子：冻结、ref、路径、大小
- [x] 看板：最后推送、断档、agent 状态
- [x] 报表 CSV
- [x] 备份与恢复

## 判题

- [x] `examd judge` CLI
- [x] clone/checkout/final_commit 校验
- [x] 编译容器
- [x] 运行容器
- [x] 用例比对
- [x] 结果落库与 CSV

## 演练

- [x] 10 人模拟（本机 Gitea 1.22.6：开通 10 人，10 人并发推送 3 秒完成）
- [x] 70 台真机 push（软件端 70 客户端并发压测：开通 39.8s、推送 18.3s、70/70 有推送；真机现场另行演练）
- [x] 冻结演练（final commit 与 Gitea 分支头一致，冻结后推送被拒）
- [x] 断电恢复（强杀 examd 后重启数据正常；备份包恢复到全新目录后报表一致）
- [x] 断网演练（停 Gitea 20 秒：agent 指数退避重试，恢复后自动补推成功）

## 待定

- [x] agent 还是检查点模式 → agent（agent 停摆时考生可手动 git commit/push，白名单同样生效，见 docs/agent.md）
- [x] Gitea 还是裸仓库 → Gitea
- [x] 变体按批次还是按学号 → 名单 `variant` 列，按人或按批都能表达
- [x] 判题批量还是推送触发 → 批量（`examd judge`）
