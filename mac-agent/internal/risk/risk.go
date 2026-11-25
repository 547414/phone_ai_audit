package risk

import (
	"regexp"
	"strings"
)

type Result struct {
	Level   string
	Reasons []string
}

type rule struct {
	re     *regexp.Regexp
	reason string
}

var highRules = []rule{
	{regexp.MustCompile(`(?i)\brm\s+-[a-z]*r[a-z]*f|\brm\s+-[a-z]*f[a-z]*r`), "包含强制递归删除 (rm -rf)"},
	{regexp.MustCompile(`(?i)\bgit\s+push\b.*(--force|-f\b|--force-with-lease)`), "包含 Git 强制推送"},
	{regexp.MustCompile(`(?i)\bgit\s+reset\s+--hard\b|\bgit\s+clean\s+-[a-z]*f`), "包含破坏性 Git 重置/清理"},
	{regexp.MustCompile(`(?i)\bsudo\b`), "包含 sudo 提权命令"},
	{regexp.MustCompile(`(?i)\b(drop\s+table|drop\s+database|truncate\s+table|delete\s+from)\b`), "包含高危数据库变更/删除语句"},
	{regexp.MustCompile(`(?i)(curl|wget)[^|\n]+\|\s*(ba)?sh\b`), "包含远程脚本直接管道执行"},
	{regexp.MustCompile(`(?i)\bchmod\s+777\b|\bmkfs\b|\bdd\s+if=`), "包含高危系统权限/磁盘操作"},
	{regexp.MustCompile(`(?i)(^|[^a-z0-9])(prod|production)([^a-z0-9]|$)`), "涉及生产环境 (prod) 目标"},
}

var mediumRules = []rule{
	{regexp.MustCompile(`(?i)\b(ssh|scp|rsync|docker|kubectl|systemctl|nginx|certbot)\b`), "涉及远程主机或系统服务操作"},
	{regexp.MustCompile(`(?i)\bgit\s+(push|rebase|merge|checkout)\b`), "涉及 Git 仓库变更"},
	{regexp.MustCompile(`(?i)\b(brew|npm|pip|uv|apt|yarn|pnpm)\s+(install|uninstall|remove|upgrade)\b`), "涉及软件包安装或卸载"},
	{regexp.MustCompile(`(?i)\b(curl|wget|http)\b`), "涉及网络请求"},
}

func Evaluate(toolName, commandOrText string, bypassSandbox, outsideWorkspace bool) Result {
	var reasons []string
	level := "low"

	if bypassSandbox {
		level = "high"
		reasons = append(reasons, "请求绕过终端沙箱执行 (BypassSandbox)")
	}
	if outsideWorkspace {
		level = "high"
		reasons = append(reasons, "修改工作区外部文件")
	}

	for _, r := range highRules {
		if r.re.MatchString(commandOrText) {
			level = "high"
			reasons = append(reasons, r.reason)
		}
	}

	if level != "high" {
		for _, r := range mediumRules {
			if r.re.MatchString(commandOrText) {
				level = "medium"
				reasons = append(reasons, r.reason)
			}
		}
		tl := strings.ToLower(toolName)
		if level == "low" && (tl == "bash" || tl == "run_command" || tl == "shell" || tl == "exec_command") {
			level = "medium"
			reasons = append(reasons, "执行本机 Shell 命令")
		}
	}

	return Result{Level: level, Reasons: unique(reasons)}
}

func unique(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
