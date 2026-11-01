// examd 是考场服务端：Gitea 开通、题面、看板、报表、备份与判题 CLI。
package main

import (
	"os"

	"exam.local/tools/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
