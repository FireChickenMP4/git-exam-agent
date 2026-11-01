// exam-agent 是考生本机的自动提交与推送程序。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"exam.local/tools/internal/agent"
)

var version = "dev"

func main() {
	var (
		configPath  = flag.String("config", ".exam-agent.json", "配置文件路径")
		initRepo    = flag.Bool("init", false, "首次把远端仓库拉到 workspace（不需要本机 git）")
		once        = flag.Bool("once", false, "只提交并推送一次（检查点模式）")
		showVersion = flag.Bool("version", false, "打印版本")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	cfg, err := agent.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("配置错误: %v", err)
	}

	log.SetFlags(0)
	log.SetPrefix("[exam-agent] ")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *initRepo {
		if err := agent.InitWorkspace(ctx, cfg, os.Stdout); err != nil {
			log.Fatalf("初始化失败: %v", err)
		}
		return
	}

	if *once {
		if err := agent.RunOnce(ctx, cfg, os.Stdout); err != nil {
			log.Fatalf("检查点失败: %v", err)
		}
		return
	}

	if err := agent.Run(ctx, cfg, os.Stdout); err != nil {
		log.Fatalf("运行失败: %v", err)
	}
}
