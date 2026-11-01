# 判题镜像：固定 Go 1.26，编译与运行都在容器内。
# 构建：make judge-image （等价于 docker build -f deploy/judge.Dockerfile -t exam-go:1.26 deploy）
#
# examd 在这个镜像里执行：
#   go build -o /out/main ./problems/<key>
# 并以 /out/main 运行隐藏用例，工作目录只挂载考生代码。
FROM golang:1.26-alpine

ENV CGO_ENABLED=0 \
    GOPROXY=off \
    GOCACHE=/tmp/gocache \
    GOPATH=/tmp/gopath

WORKDIR /src
