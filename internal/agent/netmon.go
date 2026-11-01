package agent

import (
	"net"
	"sort"
	"strings"
)

// networkSignature 返回当前非回环、已启用网卡的地址指纹。
// 考场所有 AP 使用同一 SSID，接口地址变化足以代表网络切换。
func networkSignature() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "unknown"
	}
	var parts []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			parts = append(parts, iface.Name+"/"+ip.String())
		}
	}
	if len(parts) == 0 {
		return "offline"
	}
	sort.Strings(parts)
	signature := strings.Join(parts, ",")
	if ssid := cachedSSID(); ssid != "" {
		return "wifi=" + ssid + "|" + signature
	}
	return signature
}
