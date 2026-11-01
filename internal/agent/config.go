// Package agent 实现考生本机的自动提交、推送与心跳。
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// 默认值与 PLAN.md 7.3 的配置示例保持一致。
const (
	defaultDebounceMS        = 3000
	defaultPushIntervalMSMin = 10000
	defaultPushIntervalMSMax = 30000
	defaultHeartbeatMSMin    = 55000
	defaultHeartbeatMSMax    = 65000
	defaultBulkEditBytes     = 4096
)

// Config 对应 .exam-agent.json。
type Config struct {
	Repo               string `json:"repo"`
	Username           string `json:"username"`
	Token              string `json:"token"`
	Workspace          string `json:"workspace"`
	ExamdURL           string `json:"examd_url"`
	CAFile             string `json:"ca_file"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
	BulkEditBytes      int64  `json:"bulk_edit_bytes"`
	DebounceMS         int    `json:"debounce_ms"`
	PushIntervalMSMin  int    `json:"push_interval_ms_min"`
	PushIntervalMSMax  int    `json:"push_interval_ms_max"`
	HeartbeatMSMin     int    `json:"heartbeat_ms_min"`
	HeartbeatMSMax     int    `json:"heartbeat_ms_max"`

	// dir 是配置文件所在目录，相对路径的 workspace 以它为基准。
	dir string
}

// LoadConfig 读取并校验配置文件。
func LoadConfig(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析配置路径: %w", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("读取配置 %s: %w", abs, err)
	}

	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置 %s: %w", abs, err)
	}
	cfg.dir = filepath.Dir(abs)
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Workspace == "" {
		c.Workspace = "."
	}
	if c.DebounceMS <= 0 {
		c.DebounceMS = defaultDebounceMS
	}
	if c.BulkEditBytes == 0 {
		c.BulkEditBytes = defaultBulkEditBytes
	}
	if c.PushIntervalMSMin <= 0 {
		c.PushIntervalMSMin = defaultPushIntervalMSMin
	}
	if c.PushIntervalMSMax <= 0 {
		c.PushIntervalMSMax = defaultPushIntervalMSMax
	}
	if c.HeartbeatMSMin <= 0 {
		c.HeartbeatMSMin = defaultHeartbeatMSMin
	}
	if c.HeartbeatMSMax <= 0 {
		c.HeartbeatMSMax = defaultHeartbeatMSMax
	}
}

// Validate 检查必填项与区间关系。
func (c *Config) Validate() error {
	switch {
	case c.Repo == "":
		return fmt.Errorf("配置缺少 repo")
	case c.Username == "":
		return fmt.Errorf("配置缺少 username")
	case c.Token == "":
		return fmt.Errorf("配置缺少 token")
	case c.DebounceMS < 100:
		return fmt.Errorf("debounce_ms 不能小于 100")
	case c.PushIntervalMSMin > c.PushIntervalMSMax:
		return fmt.Errorf("push_interval_ms_min 不能大于 push_interval_ms_max")
	case c.HeartbeatMSMin > c.HeartbeatMSMax:
		return fmt.Errorf("heartbeat_ms_min 不能大于 heartbeat_ms_max")
	}
	if c.CAFile != "" {
		if _, err := os.Stat(c.CAFilePath()); err != nil {
			return fmt.Errorf("读取 ca_file %s: %w", c.CAFilePath(), err)
		}
	}
	return nil
}

// CAFilePath 返回 CA 文件的绝对路径。
func (c *Config) CAFilePath() string {
	if c.CAFile == "" || filepath.IsAbs(c.CAFile) {
		return c.CAFile
	}
	return filepath.Join(c.dir, c.CAFile)
}

// WorkspaceDir 返回工作区绝对路径。
func (c *Config) WorkspaceDir() (string, error) {
	dir := c.Workspace
	if !filepath.IsAbs(dir) {
		base := c.dir
		if base == "" {
			var err error
			if base, err = os.Getwd(); err != nil {
				return "", fmt.Errorf("获取当前目录: %w", err)
			}
		}
		dir = filepath.Join(base, dir)
	}
	return filepath.Abs(dir)
}

// ConfigDir 返回配置文件所在目录，供事件日志定位。
func (c *Config) ConfigDir() string {
	if c.dir != "" {
		return c.dir
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// Debounce 返回防抖时长。
func (c *Config) Debounce() time.Duration {
	return time.Duration(c.DebounceMS) * time.Millisecond
}

// PushInterval 返回普通推送的随机区间。
func (c *Config) PushInterval() (time.Duration, time.Duration) {
	return time.Duration(c.PushIntervalMSMin) * time.Millisecond,
		time.Duration(c.PushIntervalMSMax) * time.Millisecond
}

// Heartbeat 返回心跳提交的随机区间。
func (c *Config) Heartbeat() (time.Duration, time.Duration) {
	return time.Duration(c.HeartbeatMSMin) * time.Millisecond,
		time.Duration(c.HeartbeatMSMax) * time.Millisecond
}
