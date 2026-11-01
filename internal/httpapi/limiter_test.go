package httpapi

import (
	"testing"
	"time"
)

func TestLimiterBurstAndRefill(t *testing.T) {
	now := time.Now()
	l := newLimiter(2, time.Minute)
	l.now = func() time.Time { return now }

	if !l.Allow("a") || !l.Allow("a") {
		t.Fatal("前两次请求应允许")
	}
	if l.Allow("a") {
		t.Fatal("第三次请求应被限流")
	}
	if !l.Allow("b") {
		t.Fatal("不同 key 不应共享额度")
	}

	now = now.Add(31 * time.Second)
	if !l.Allow("a") {
		t.Fatal("窗口补充后应允许一次")
	}
	if l.Allow("a") {
		t.Fatal("补充的额度只够一次")
	}
}
