# 题目目录与判题约定

## 目录格式

```text
problems/
  sum/
    problem.md          # 默认题面（Markdown，可选）
    meta.json           # title / score / time_limit_ms / memory_limit_mb（可选）
    variants/
      A.md              # 变体题面，文件名即 seed（名单里的 variant 列）
      B.md
    cases/
      01.in
      01.out            # 隐藏用例，必须成对
      02.in
      02.out
    samples/
      01.in
      01.out            # 样例，仅展示，不计分
```

导入：

```bash
examd problem -config deploy/config.yaml -exam 2027-spring -dir problems/
```

导入会把用例复制到 `data/problems/<场次>/<题目>/`，数据库只存相对路径；判题时由服务器读取输入与期望输出，考生容器里看不到期望输出。

`meta.json` 缺省值：`title` 同目录名、`score=100`、`time_limit_ms=3000`、`memory_limit_mb=256`。隐藏用例按文件名排序，分值平均分配（余数给前面的用例）。

## 代码布局

考生仓库模板只有根目录 `main.go`。正式答题时每道题放在自己的包里：

```text
exam-2027-20231234/
  go.mod
  main.go
  problems/
    sum/main.go
    reverse/main.go
```

判题时优先编译 `problems/<题目 key>`；该目录不存在时退回仓库根目录 `.`（适合只有一道题的场次）。

程序从标准输入读、向标准输出写。判题比较时会忽略行尾空白与末尾空行，其它字符必须完全一致。

运行容器是只读根文件系统，只提供可写的 `/tmp`（64MB）；程序不要依赖在当前目录写文件。

## 评分

- 每个隐藏用例有独立分值，通过即得分。
- 题目状态：`accepted`（全过）、`partial`（部分）、`failed`（全错）、`compile_error`、`timeout`、`output_limit`（输出超过 1MB 被终止）、`missing_solution`、`no_cases`。
- 考生总分 = 各题得分之和。

## 判题运行限制

| 阶段 | 限制 |
|---|---|
| 编译 | 1GB 内存、10 秒、并发 2 |
| 运行 | 256MB 内存、题目 `time_limit_ms`（默认 3 秒）、并发 8 |

判题容器无网络（`--network none`），`GOPROXY=off`，只允许标准库或仓库内 `vendor/`。

## 本地调试

开发机上不装 Docker 时：

```bash
examd judge -config deploy/config.yaml -exam 2027-spring -out results.csv -runtime local
```

`local` 模式直接调用本机 `go build` / 二进制，不施加内存与网络限制，仅用于开发。配置文件里使用
`judge.runtime: local` 时必须同时写 `judge.allow_local: true`；CLI 的 `-runtime local` 属于显式覆盖。
