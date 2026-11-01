package agent

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

var (
	ssidMu    sync.Mutex
	ssidValue string
	ssidAt    time.Time
)

// cachedSSID 每 30 秒最多探测一次无线 SSID，避免频繁拉起系统命令。
func cachedSSID() string {
	ssidMu.Lock()
	defer ssidMu.Unlock()
	if time.Since(ssidAt) < 30*time.Second {
		return ssidValue
	}
	ssidValue = detectSSID()
	ssidAt = time.Now()
	return ssidValue
}

func runShort(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// detectSSID 尽力读取当前无线网络名；读不到返回空串，不影响网卡/IP 检测。
func detectSSID() string {
	switch runtime.GOOS {
	case "windows":
		return parseNetshSSID(runShort("netsh", "wlan", "show", "interfaces"))
	case "darwin":
		airport := runShort("/System/Library/PrivateFrameworks/Apple80211.framework/Versions/Current/Resources/airport", "-I")
		if ssid := parseAirportSSID(airport); ssid != "" {
			return ssid
		}
		return parseNetworksetupSSID(runShort("networksetup", "-getairportnetwork", "en0"))
	case "linux":
		if ssid := strings.TrimSpace(runShort("iwgetid", "-r")); ssid != "" {
			return ssid
		}
		return parseNmcliSSID(runShort("nmcli", "-t", "-f", "active,ssid", "dev", "wifi"))
	}
	return ""
}

func parseNetshSSID(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if !strings.HasPrefix(line, "SSID") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) != "SSID" {
			continue
		}
		if ssid := strings.TrimSpace(parts[1]); ssid != "" {
			return ssid
		}
	}
	return ""
}

func parseAirportSSID(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if !strings.HasPrefix(line, "SSID:") {
			continue
		}
		if ssid := strings.TrimSpace(strings.TrimPrefix(line, "SSID:")); ssid != "" {
			return ssid
		}
	}
	return ""
}

func parseNetworksetupSSID(out string) string {
	line := strings.TrimSpace(strings.TrimSuffix(out, "\r\n"))
	if line == "" || !strings.Contains(line, ":") {
		return ""
	}
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func parseNmcliSSID(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && parts[0] == "yes" {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}
