// Package config 读取 examd 配置文件（YAML，支持 ${ENV} 展开）。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是 examd 的完整配置。
type Config struct {
	Listen    string `yaml:"listen"`
	PublicURL string `yaml:"public_url"`
	DB        string `yaml:"db"`
	DataDir   string `yaml:"data_dir"`
	Gitea     Gitea  `yaml:"gitea"`
	Judge     Judge  `yaml:"judge"`
	Exam      Exam   `yaml:"exam"`

	dir string
}

// Gitea 是 Gitea 实例与开通相关配置。
type Gitea struct {
	URL                string `yaml:"url"`
	AdminUser          string `yaml:"admin_user"`
	AdminPassword      string `yaml:"admin_password"`
	AdminToken         string `yaml:"admin_token"`
	WebhookSecret      string `yaml:"webhook_secret"`
	WebhookURL         string `yaml:"webhook_url"`
	HookToken          string `yaml:"hook_token"`
	CAFile             string `yaml:"ca_file"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
	AllowUnknownRepos  bool   `yaml:"allow_unknown_repos"`
}

// Judge 是判题运行器配置。
type Judge struct {
	Runtime               string `yaml:"runtime"` // docker | local
	AllowLocal            bool   `yaml:"allow_local"`
	Image                 string `yaml:"image"`
	GoBin                 string `yaml:"go_bin"`
	WorkDir               string `yaml:"work_dir"`
	HostWorkDir           string `yaml:"host_work_dir"`
	CompileTimeoutSeconds int    `yaml:"compile_timeout_seconds"`
	RunTimeoutMS          int    `yaml:"run_timeout_ms"`
	MemoryMB              int    `yaml:"memory_mb"`
	CompileConcurrency    int    `yaml:"compile_concurrency"`
	RunConcurrency        int    `yaml:"run_concurrency"`
}

// Exam 是考试行为配置。
type Exam struct {
	FreezeGraceSeconds      int    `yaml:"freeze_grace_seconds"`
	ConsentVersion          string `yaml:"consent_version"`
	AutoFreeze              bool   `yaml:"auto_freeze"`
	RepoPrefix              string `yaml:"repo_prefix"`
	Timezone                string `yaml:"timezone"`
	ProblemVisibleBeforeMin int    `yaml:"problem_visible_before_minutes"`
}

func defaults() *Config {
	return &Config{
		Listen:    ":8080",
		PublicURL: "http://localhost:8080",
		DB:        "data/examd.db",
		DataDir:   "data",
		Gitea: Gitea{
			AdminUser:         "exam-admin",
			AllowUnknownRepos: true,
		},
		Judge: Judge{
			Runtime:               "docker",
			Image:                 "exam-go:1.26",
			GoBin:                 "go",
			WorkDir:               "data/judge",
			CompileTimeoutSeconds: 10,
			RunTimeoutMS:          3000,
			MemoryMB:              256,
			CompileConcurrency:    2,
			RunConcurrency:        8,
		},
		Exam: Exam{
			FreezeGraceSeconds: 30,
			ConsentVersion:     "v1",
			AutoFreeze:         true,
			RepoPrefix:         "exam-2027-",
			Timezone:           "Asia/Shanghai",
		},
	}
}

// Load 读取配置文件，缺失项使用默认值。
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析配置路径: %w", err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("读取配置 %s: %w", abs, err)
	}
	cfg := defaults()
	if err := yaml.Unmarshal([]byte(os.ExpandEnv(string(raw))), cfg); err != nil {
		return nil, fmt.Errorf("解析配置 %s: %w", abs, err)
	}
	cfg.dir = filepath.Dir(abs)
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	switch {
	case c.Listen == "":
		return fmt.Errorf("listen 不能为空")
	case c.DB == "":
		return fmt.Errorf("db 不能为空")
	case c.DataDir == "":
		return fmt.Errorf("data_dir 不能为空")
	case c.Judge.Runtime != "docker" && c.Judge.Runtime != "local":
		return fmt.Errorf("judge.runtime 只能是 docker 或 local")
	case c.Judge.Runtime == "local" && !c.Judge.AllowLocal:
		return fmt.Errorf("judge.runtime=local 需要显式设置 judge.allow_local: true")
	case c.Judge.Runtime == "docker" && c.Judge.Image == "":
		return fmt.Errorf("judge.runtime=docker 时 judge.image 不能为空")
	case c.Judge.CompileConcurrency < 1:
		return fmt.Errorf("judge.compile_concurrency 至少为 1")
	case c.Judge.RunConcurrency < 1:
		return fmt.Errorf("judge.run_concurrency 至少为 1")
	case c.Exam.FreezeGraceSeconds < 0:
		return fmt.Errorf("exam.freeze_grace_seconds 不能为负")
	}
	if c.Exam.Timezone != "" {
		if _, err := time.LoadLocation(c.Exam.Timezone); err != nil {
			return fmt.Errorf("exam.timezone %q 无效: %w", c.Exam.Timezone, err)
		}
	}
	return nil
}

// ConfigDir 返回配置文件目录，相对路径以此为基准。
func (c *Config) ConfigDir() string {
	if c.dir != "" {
		return c.dir
	}
	return "."
}

func (c *Config) resolve(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.ConfigDir(), p)
}

// DBPath 返回数据库绝对路径。
func (c *Config) DBPath() string { return c.resolve(c.DB) }

// DataDirPath 返回数据目录绝对路径。
func (c *Config) DataDirPath() string { return c.resolve(c.DataDir) }

// JudgeWorkDir 返回判题临时目录绝对路径。
func (c *Config) JudgeWorkDir() string { return c.resolve(c.Judge.WorkDir) }

// JudgeHostWorkDir 返回判题目录在宿主机上的对应路径（examd 在容器内时使用）。
func (c *Config) JudgeHostWorkDir() string {
	if c.Judge.HostWorkDir == "" {
		return ""
	}
	return c.resolve(c.Judge.HostWorkDir)
}

// Location 返回考试时区；未配置时使用本机时区。
func (c *Config) Location() (*time.Location, error) {
	tz := c.Exam.Timezone
	if tz == "" {
		return time.Local, nil
	}
	return time.LoadLocation(tz)
}

// CompileTimeout 返回编译超时。
func (c *Config) CompileTimeout() time.Duration {
	return time.Duration(c.Judge.CompileTimeoutSeconds) * time.Second
}

// RunTimeout 返回单个用例的默认超时。
func (c *Config) RunTimeout() time.Duration {
	return time.Duration(c.Judge.RunTimeoutMS) * time.Millisecond
}

// SessionSecretMissing 检查 webhook / hook 密钥是否已配置。
func (c *Config) SessionSecretMissing() []string {
	var missing []string
	if c.Gitea.WebhookSecret == "" {
		missing = append(missing, "gitea.webhook_secret")
	}
	if c.Gitea.HookToken == "" {
		missing = append(missing, "gitea.hook_token")
	}
	return missing
}
