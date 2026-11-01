package agent

import "testing"

func TestParseNetshSSID(t *testing.T) {
	out := `
    Name                   : WLAN
    Description            : Intel(R) Wi-Fi
    State                  : connected
    SSID                   : exam-ap
    BSSID                  : aa:bb:cc:dd:ee:ff
`
	if got := parseNetshSSID(out); got != "exam-ap" {
		t.Fatalf("netsh SSID = %q", got)
	}
	if got := parseNetshSSID("\n    SSID                   : \n"); got != "" {
		t.Fatalf("空 SSID 应返回空串，得到 %q", got)
	}
}

func TestParseAirportSSID(t *testing.T) {
	out := "     agrCtlRSSI: -58\n     SSID: exam-ap-5g\n     BSSID: aa:bb\n"
	if got := parseAirportSSID(out); got != "exam-ap-5g" {
		t.Fatalf("airport SSID = %q", got)
	}
}

func TestParseNetworksetupSSID(t *testing.T) {
	if got := parseNetworksetupSSID("Current Wi-Fi Network: exam-ap\n"); got != "exam-ap" {
		t.Fatalf("networksetup SSID = %q", got)
	}
	if got := parseNetworksetupSSID("You are not associated with an AirPort network.\n"); got != "" {
		t.Fatalf("非无线网络时应返回空串，得到 %q", got)
	}
}

func TestParseNmcliSSID(t *testing.T) {
	out := "no:other\nno:guest\nyes:exam-ap\n"
	if got := parseNmcliSSID(out); got != "exam-ap" {
		t.Fatalf("nmcli SSID = %q", got)
	}
	if got := parseNmcliSSID(""); got != "" {
		t.Fatalf("空输出应返回空串")
	}
}
