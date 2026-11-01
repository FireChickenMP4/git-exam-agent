package main

import (
	"bufio"
	"fmt"
	"os"
)

// 在这里写你的解法。
// 每个题目放在 problems/<题目 key>/ 下，读标准输入，写标准输出。
func main() {
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		fmt.Println(in.Text())
	}
}
