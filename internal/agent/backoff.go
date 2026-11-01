package agent

import "time"

const (
	backoffBase = 2 * time.Second
	backoffMax  = 60 * time.Second
)

// backoffDelay 返回第 attempts 次失败后的等待时长：2s、4s、8s……上限 60s。
func backoffDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := backoffBase
	for i := 1; i < attempts; i++ {
		delay *= 2
		if delay >= backoffMax {
			return backoffMax
		}
	}
	return delay
}
