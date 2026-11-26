package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"phone-ai-audit/mac-agent/internal/agyauth"
)

func printAgyHelp() {
	fmt.Println(`Antigravity (agy) 多账号切换与新开终端（凭据安全保存在 macOS Keychain，本地不落盘明文 Token）:

  ai-audit agy                                    列出已保存的 agy 账号、当前全局状态、到期提醒及实时配额
  ai-audit agy ls                                 同上
  ai-audit agy save [别名] [--email <邮箱>] [--ttl <时长>]
                                                  将当前全局已登录的 agy 账号保存到钥匙串档案
  ai-audit agy login <新别名> [--cwd <目录>] [--ttl <时长>] [--force]
                                                  安全清空当前登录槽位、弹出新终端登录新号并自动保存档案
  ai-audit agy ttl <别名或邮箱> <时长>            自定义账号登录到期/续登提醒周期（如 7d、14d、30d、24h、auto、off）
  ai-audit agy switch <别名或邮箱> [--force]      仅切换全局默认 agy 账号（写入系统 Keychain）
  ai-audit agy open <别名或邮箱> [选项] [-- agy参数...]
                                                  一键切换全局账号并弹出新的 tmux 终端运行 agy
  ai-audit agy <别名或邮箱> [选项]                ai-audit agy open 的快捷简写
  ai-audit agy rm <别名或邮箱>                    删除已保存的账号档案（不影响当前正在运行的终端）

open 可选参数:
  --cwd <目录>     指定新终端的工作目录（默认当前目录）
  --here           在当前终端直接运行 agy，不另弹新窗口
  --attach         若该账号在当前目录已有同名 tmux 会话，直接切入而不是新开编号会话
  --force / -f     忽略已过期拦截提示，强制继续切换/打开
  --no-wait        切换并拉起终端后立即返回，不等待 1.5s 启动保护窗口

到期提醒说明:
  - 保存档案时会自动解析凭据内嵌的过期时间；若含 RefreshToken 且无显式过期字段，默认 14 天后提醒续登
  - 可用 --ttl（如 7d / 30d / off）随时自定义；快到期时自动提醒，已过期时切换/打开会二次确认防混乱

首次配置双账号推荐步骤:
  1. 先把当前已登录账号保存:     ai-audit agy save main
  2. 登录并自动保存第二个账号:   ai-audit agy login work
  3. 以后随时一键新开指定号终端: ai-audit agy work   或   ai-audit agy open main --cwd ~/project`)
}

func cmdAgy(args []string) {
	mgr := agyauth.New(agyauth.Options{})

	if len(args) == 0 {
		cmdAgyList(mgr)
		return
	}

	sub := args[0]
	rest := args[1:]
	switch sub {
	case "help", "-h", "--help":
		printAgyHelp()
	case "ls", "list":
		cmdAgyList(mgr)
	case "save", "add":
		cmdAgySave(mgr, rest)
	case "login":
		cmdAgyLogin(mgr, rest)
	case "ttl", "expire":
		cmdAgyTTL(mgr, rest)
	case "switch", "use":
		cmdAgySwitch(mgr, rest)
	case "open", "run":
		cmdAgyOpen(mgr, rest)
	case "rm", "delete", "del":
		cmdAgyRemove(mgr, rest)
	default:
		if !strings.HasPrefix(sub, "-") && mgr.HasProfile(sub) {
			cmdAgyOpen(mgr, args)
			return
		}
		fmt.Fprintf(os.Stderr, "未知子命令或未找到账号档案: %s\n\n", sub)
		printAgyHelp()
		os.Exit(1)
	}
}

func cmdAgyList(mgr *agyauth.Manager) {
	list, active, err := mgr.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取 agy 账号列表失败: %v\n", err)
		os.Exit(1)
	}
	if len(list) == 0 {
		fmt.Println("当前尚未保存任何 Antigravity (agy) 账号档案。")
		if active != nil && active.Present {
			hint := ""
			if active.EmailHint != "" {
				hint = "（检测到邮箱: " + active.EmailHint + "）"
			}
			fmt.Printf("检测到系统 Keychain 已有当前登录态%s，可先运行:\n  ai-audit agy save main\n", hint)
		} else {
			fmt.Println("请先运行 agy 完成一次登录，再执行 `ai-audit agy save <别名>` 保存档案。")
		}
		return
	}

	fmt.Println("Antigravity 账号档案（凭据加密保存在 macOS Keychain）:")
	anyActive := false
	var warnings []string
	for _, st := range list {
		mark := " "
		if st.Active {
			mark = "*"
			anyActive = true
		}
		emailPart := st.Email
		if emailPart == "" {
			emailPart = "(未标注邮箱)"
		}
		planPart := ""
		if st.PlanTier != "" {
			planPart = " [" + st.PlanTier + "]"
		}
		quotaParts := make([]string, 0, 2)
		if st.Quota5h != "" {
			quotaParts = append(quotaParts, "5h "+st.Quota5h)
		}
		if st.QuotaWeekly != "" {
			quotaParts = append(quotaParts, "周 "+st.QuotaWeekly)
		}
		quotaStr := ""
		if len(quotaParts) > 0 {
			quotaStr = "  " + strings.Join(quotaParts, " · ")
		}
		fmt.Printf("  %s %-14s %-28s%s%s\n", mark, st.Name, emailPart, planPart, quotaStr)
		if st.Expiry.Summary != "" {
			prefix := "    登录状态: "
			if st.Expiry.Expired {
				prefix = "    ✖ 登录状态: "
			} else if st.Expiry.ExpiringSoon {
				prefix = "    ⚠ 登录状态: "
			}
			fmt.Printf("%s%s\n", prefix, st.Expiry.Summary)
		}
		if st.Expiry.Warning != "" {
			warnings = append(warnings, fmt.Sprintf("  - [%s] %s", st.Name, st.Expiry.Warning))
		}
	}
	if len(warnings) > 0 {
		fmt.Println("\n⚠ 到期提醒:")
		for _, w := range warnings {
			fmt.Println(w)
		}
	}
	if active != nil && active.Present && !anyActive {
		hint := ""
		if active.EmailHint != "" {
			hint = " (" + active.EmailHint + ")"
		}
		fmt.Printf("\n提示: 当前系统 Keychain 活跃账号%s尚未保存到档案，可执行 `ai-audit agy save <别名>` 备份。\n", hint)
	}
	fmt.Println("\n快捷打开: ai-audit agy <别名或邮箱>   |   新增/重登: ai-audit agy login <别名>   |   改周期: ai-audit agy ttl <别名> 7d")
}

func cmdAgySave(mgr *agyauth.Manager, args []string) {
	alias := ""
	opts := agyauth.SaveOptions{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--email" && i+1 < len(args):
			i++
			opts.Email = args[i]
		case strings.HasPrefix(a, "--email="):
			opts.Email = strings.TrimPrefix(a, "--email=")
		case (a == "--ttl" || a == "--expire") && i+1 < len(args):
			i++
			ttlMs, err := agyauth.ParseDurationExtended(args[i])
			if err != nil {
				fmt.Fprintf(os.Stderr, "解析 --ttl 失败: %v\n", err)
				os.Exit(1)
			}
			opts.CustomTTLMs = &ttlMs
		case strings.HasPrefix(a, "--ttl=") || strings.HasPrefix(a, "--expire="):
			raw := strings.TrimPrefix(strings.TrimPrefix(a, "--ttl="), "--expire=")
			ttlMs, err := agyauth.ParseDurationExtended(raw)
			if err != nil {
				fmt.Fprintf(os.Stderr, "解析 --ttl 失败: %v\n", err)
				os.Exit(1)
			}
			opts.CustomTTLMs = &ttlMs
		case a == "-h" || a == "--help":
			printAgyHelp()
			return
		case !strings.HasPrefix(a, "-") && alias == "":
			alias = a
		}
	}
	prof, err := mgr.SaveCurrentWithOptions(alias, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "保存失败: %v\n", err)
		os.Exit(1)
	}
	exp := agyauth.ComputeExpiryState(prof, time.Now().UnixMilli())
	if prof.Email != "" {
		fmt.Printf("✓ 已将当前 agy 登录凭据保存到 macOS Keychain 档案: %s (%s)\n", prof.Name, prof.Email)
	} else {
		fmt.Printf("✓ 已将当前 agy 登录凭据保存到 macOS Keychain 档案: %s\n", prof.Name)
		fmt.Printf("  提示: 可用 `ai-audit agy save %s --email 你的邮箱` 补充邮箱标注以便关联配额显示。\n", prof.Name)
	}
	if exp.Summary != "" {
		fmt.Printf("  登录到期状态: %s（可用 `ai-audit agy ttl %s 7d` 自定义提醒周期）\n", exp.Summary, prof.Name)
	}
}

func cmdAgyTTL(mgr *agyauth.Manager, args []string) {
	if len(args) < 2 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(os.Stderr, "用法: ai-audit agy ttl <别名或邮箱> <时长，如 7d / 14d / 30d / 24h / auto / off>")
		os.Exit(1)
	}
	ttlMs, err := agyauth.ParseDurationExtended(args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "无效时长: %v\n", err)
		os.Exit(1)
	}
	prof, exp, err := mgr.SetTTL(args[0], ttlMs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "更新到期周期失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ 已更新账号 %s 的登录提醒周期 -> %s\n", prof.Name, exp.Summary)
}

func cmdAgyLogin(mgr *agyauth.Manager, args []string) {
	alias := ""
	cwd := ""
	force := false
	var customTTL *int64
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--force" || a == "-f":
			force = true
		case a == "--cwd" && i+1 < len(args):
			i++
			cwd = args[i]
		case strings.HasPrefix(a, "--cwd="):
			cwd = strings.TrimPrefix(a, "--cwd=")
		case (a == "--ttl" || a == "--expire") && i+1 < len(args):
			i++
			ttlMs, err := agyauth.ParseDurationExtended(args[i])
			if err != nil {
				fmt.Fprintf(os.Stderr, "解析 --ttl 失败: %v\n", err)
				os.Exit(1)
			}
			customTTL = &ttlMs
		case strings.HasPrefix(a, "--ttl=") || strings.HasPrefix(a, "--expire="):
			raw := strings.TrimPrefix(strings.TrimPrefix(a, "--ttl="), "--expire=")
			ttlMs, err := agyauth.ParseDurationExtended(raw)
			if err != nil {
				fmt.Fprintf(os.Stderr, "解析 --ttl 失败: %v\n", err)
				os.Exit(1)
			}
			customTTL = &ttlMs
		case a == "-h" || a == "--help":
			printAgyHelp()
			return
		case !strings.HasPrefix(a, "-") && alias == "":
			alias = a
		}
	}
	if alias == "" {
		fmt.Fprintln(os.Stderr, "用法: ai-audit agy login <新账号别名> [--cwd <目录>] [--ttl <时长>] [--force]")
		os.Exit(1)
	}
	prof, sessName, err := mgr.LoginNew(alias, cwd, force, customTTL, 3*time.Minute, func(msg string) {
		fmt.Println(msg)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "登录新增账号失败: %v\n", err)
		os.Exit(1)
	}
	exp := agyauth.ComputeExpiryState(prof, time.Now().UnixMilli())
	label := prof.Name
	if prof.Email != "" {
		label = fmt.Sprintf("%s (%s)", prof.Name, prof.Email)
	}
	if sessName != "" {
		fmt.Printf("✓ 新账号登录成功并已自动保存档案: %s（tmux 会话: %s）\n", label, sessName)
	} else {
		fmt.Printf("✓ 新账号登录成功并已自动保存档案: %s\n", label)
	}
	if exp.Summary != "" {
		fmt.Printf("  登录到期状态: %s\n", exp.Summary)
	}
}

func checkExpiryGuard(mgr *agyauth.Manager, query string, force bool) bool {
	prof, exp, err := mgr.InspectProfile(query)
	if err != nil || prof == nil {
		return true
	}
	if exp.Expired {
		fmt.Fprintf(os.Stderr, "⚠ 警告: [%s] %s\n", prof.Name, exp.Warning)
		if force {
			return true
		}
		if fi, serr := os.Stdin.Stat(); serr == nil && (fi.Mode()&os.ModeCharDevice) != 0 {
			fmt.Fprintf(os.Stderr, "  建议先运行 `ai-audit agy login %s` 手动重新登录防混乱。仍要继续切换吗？[y/N]: ", prof.Name)
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			ans := strings.ToLower(strings.TrimSpace(line))
			if ans != "y" && ans != "yes" {
				fmt.Fprintln(os.Stderr, "已取消。请先执行 `ai-audit agy login "+prof.Name+"` 更新登录态。")
				return false
			}
			return true
		}
		fmt.Fprintf(os.Stderr, "  （非交互终端已阻止切换过期档案；若确认强制继续请加 --force）\n")
		return false
	}
	if exp.ExpiringSoon && exp.Warning != "" {
		fmt.Fprintf(os.Stderr, "⚠ 提醒: [%s] %s\n", prof.Name, exp.Warning)
	}
	return true
}

func cmdAgySwitch(mgr *agyauth.Manager, args []string) {
	query := ""
	force := false
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Fprintln(os.Stderr, "用法: ai-audit agy switch <别名或邮箱> [--force]")
			return
		case "--force", "-f":
			force = true
		default:
			if !strings.HasPrefix(a, "-") && query == "" {
				query = a
			}
		}
	}
	if query == "" {
		fmt.Fprintln(os.Stderr, "用法: ai-audit agy switch <别名或邮箱> [--force]")
		os.Exit(1)
	}
	if !checkExpiryGuard(mgr, query, force) {
		os.Exit(1)
	}
	prof, err := mgr.Switch(query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "切换失败: %v\n", err)
		os.Exit(1)
	}
	label := prof.Name
	if prof.Email != "" {
		label = fmt.Sprintf("%s (%s)", prof.Name, prof.Email)
	}
	fmt.Printf("✓ 已将全局默认 agy 账号切换为: %s\n", label)
	fmt.Println("  说明: 新启动的 agy 终端将使用该账号；已在运行的旧终端继续保持各自内存中的原账号。")
}

func cmdAgyOpen(mgr *agyauth.Manager, args []string) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(os.Stderr, "用法: ai-audit agy open <别名或邮箱> [--cwd <目录>] [--here] [--attach] [--force] [-- <agy参数...>]")
		os.Exit(1)
	}
	query := ""
	force := false
	opts := agyauth.OpenOptions{HoldLockMs: 1500}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			opts.AgyArgs = append(opts.AgyArgs, args[i+1:]...)
			break
		}
		switch {
		case a == "--cwd" && i+1 < len(args):
			i++
			opts.Cwd = args[i]
		case strings.HasPrefix(a, "--cwd="):
			opts.Cwd = strings.TrimPrefix(a, "--cwd=")
		case a == "--here" || a == "--in-place":
			opts.InPlace = true
		case a == "--attach":
			opts.Attach = true
		case a == "--force" || a == "-f":
			force = true
		case a == "--no-wait":
			opts.HoldLockMs = -1
		case !strings.HasPrefix(a, "-") && query == "":
			query = a
		default:
			opts.AgyArgs = append(opts.AgyArgs, a)
		}
	}
	if query == "" {
		fmt.Fprintln(os.Stderr, "请指定要打开的账号别名或邮箱，例如: ai-audit agy open work")
		os.Exit(1)
	}
	if !checkExpiryGuard(mgr, query, force) {
		os.Exit(1)
	}

	prof, sessName, err := mgr.Open(query, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开 agy 终端失败: %v\n", err)
		os.Exit(1)
	}
	if opts.InPlace {
		return
	}
	label := prof.Name
	if prof.Email != "" {
		label = fmt.Sprintf("%s (%s)", prof.Name, prof.Email)
	}
	if sessName != "" {
		fmt.Printf("✓ 已切换全局账号至 %s 并打开新终端（tmux 会话: %s，手机端也可直接接管）\n", label, sessName)
	} else {
		fmt.Printf("✓ 已切换全局账号至 %s 并打开新终端\n", label)
	}
}

func cmdAgyRemove(mgr *agyauth.Manager, args []string) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(os.Stderr, "用法: ai-audit agy rm <别名或邮箱>")
		os.Exit(1)
	}
	prof, err := mgr.Remove(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "删除失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ 已删除 agy 账号档案: %s\n", prof.Name)
}
