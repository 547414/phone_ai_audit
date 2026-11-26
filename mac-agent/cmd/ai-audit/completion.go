package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"phone-ai-audit/mac-agent/internal/agyauth"
)

const (
	completionMarkerStart = "# >>> phone-ai-audit completion >>>"
	completionMarkerEnd   = "# <<< phone-ai-audit completion <<<"
)

func printSubcommandHelp(sub string) {
	switch strings.ToLower(strings.TrimSpace(sub)) {
	case "", "all", "help":
		printHelp()
	case "agy":
		printAgyHelp()
	case "clean":
		fmt.Println(`用法: ai-audit clean [--all] [--dry-run]

清理后台没有窗口挂载（detached）的遗留 tmux 会话：
  ai-audit clean            仅清理已退出 AI、只剩空 shell（zsh/bash）的闲置后台会话（安全）
  ai-audit clean --all      一并清理所有后台无窗口挂载的会话（含后台挂起的 agy/claude/codex）
  ai-audit clean --dry-run  仅预览将被清理的会话，不实际销毁`)
	case "status":
		fmt.Println(`用法: ai-audit status

查看中转服务连接状态、手机在线设备数、当前审批模式、Mac 空闲/锁屏状态与本机 tmux 会话数。`)
	case "usage":
		fmt.Println(`用法: ai-audit usage

增量统计并输出 Claude Code / OpenAI Codex / Antigravity（支持多账号独立展示）的今日、5h、本周、本月、总计 Token 用量、配额剩余与重置倒计时。`)
	case "web":
		fmt.Println(`用法: ai-audit web

在默认浏览器中打开本机 Agent 用量看板网页（默认 http://127.0.0.1:9878，可在 config.json 的 web_addr 配置）。`)
	case "mode":
		fmt.Println(`用法: ai-audit mode <auto|both|phone|notify>

切换审批路由模式（Mac 与手机实时同步）：
  auto    智能离座：人在电脑前走 Mac 弹窗，离座 60s 或锁屏自动转交手机
  both    双端同步：Mac 右上角悬浮窗与手机通知栏同时弹起，先点为准、另一端秒消失
  phone   始终手机：无论是否在电脑前，全部发往手机审批
  notify  仅通知不阻塞：只发推送提醒，不拦截终端执行`)
	case "timeout":
		fmt.Println(`用法: ai-audit timeout <秒数, 5-600>

设置审批弹窗超时时长（默认 30 秒，Mac 与手机同步生效）。
示例:
  ai-audit timeout 30
  ai-audit timeout 60`)
	case "auto-approve":
		fmt.Println(`用法: ai-audit auto-approve <on|off>

开启或关闭审批弹窗超时自动确认授权（默认开启）：
  on   倒计时结束自动确认允许，并同步消除电脑和手机端弹窗且标为已读
  off  倒计时结束不自动允许，退回终端由用户手动处理`)
	case "awake":
		fmt.Println(`用法: ai-audit awake <on|off>

开启或关闭 Mac 防休眠守护（caffeinate），离座后保持后台任务与中转隧道在线。`)
	case "terminal":
		fmt.Println(`用法: ai-audit terminal <on|off>

一键开启或熔断手机端远程交互终端（tmux 反向隧道）权限。`)
	case "files":
		fmt.Println(`用法: ai-audit files <on|off>

一键开启或熔断手机端远程文件浏览、下载、编辑与 Git 工作台权限。`)
	case "pair":
		fmt.Println(`用法: ai-audit pair

在终端打印 Android App 扫码配对二维码及服务器地址 / App Token。`)
	case "test-msg":
		fmt.Println(`用法: ai-audit test-msg

向手机发送一条测试推送消息，用于验证中转与通知链路。`)
	case "test-approve":
		fmt.Println(`用法: ai-audit test-approve

发起一次测试审批请求并等待手机端或 Mac 悬浮窗做出决策。`)
	case "completion":
		fmt.Println(`用法: ai-audit completion <zsh|bash|install>

生成或一键安装终端 Tab 自动补全脚本（支持所有子命令、参数与动态补全已保存的 agy 账号别名）：
  ai-audit completion install   自动写入 ~/.config/ai-audit/completion.zsh 并在 ~/.zshrc 启用（推荐）
  ai-audit completion zsh       输出 Zsh 补全脚本到标准输出
  ai-audit completion bash      输出 Bash 补全脚本到标准输出

安装后在当前终端执行以下命令立即生效（新开的终端会自动生效）：
  source ~/.zshrc`)
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n\n", sub)
		printHelp()
		os.Exit(1)
	}
}

func cmdCompletion(args []string) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printSubcommandHelp("completion")
		return
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "zsh":
		fmt.Print(generateZshCompletion())
	case "bash":
		fmt.Print(generateBashCompletion())
	case "install":
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			fmt.Fprintln(os.Stderr, "无法获取用户 HOME 目录")
			os.Exit(1)
		}
		scriptPath, rcPath, err := installZshCompletion(home)
		if err != nil {
			fmt.Fprintf(os.Stderr, "安装 Zsh 补全失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ 已安装 Zsh Tab 自动补全脚本: %s\n", scriptPath)
		fmt.Printf("✓ 已在 %s 中启用自动加载\n", rcPath)
		fmt.Println("  提示: 新开终端自动生效；当前终端可执行 `source ~/.zshrc` 立即体验 Tab 补全！")
	default:
		fmt.Fprintf(os.Stderr, "不支持的 Shell 类型: %s（支持 zsh / bash / install）\n", args[0])
		os.Exit(1)
	}
}

// cmdInternalComplete outputs dynamic tab-completion candidates without touching macOS Keychain (<2ms).
func cmdInternalComplete(args []string) {
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "agy-profiles":
		// Read non-secret metadata file only (never prompts Keychain during Tab completion).
		for _, item := range loadAgyCompletionProfiles(agyauth.DefaultProfilesPath()) {
			fmt.Println(item)
		}
	}
}

// loadAgyCompletionProfiles returns lines of "name\tdesc" for saved agy profiles.
func loadAgyCompletionProfiles(profilesPath string) []string {
	b, err := os.ReadFile(profilesPath)
	if err != nil {
		return nil
	}
	var raw struct {
		Profiles []agyauth.Profile `json:"profiles"`
	}
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	out := make([]string, 0, len(raw.Profiles))
	for _, p := range raw.Profiles {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}
		desc := "已保存 agy 账号"
		if p.Email != "" {
			desc = p.Email
		}
		// Escape colons so zsh _describe doesn't split on description text.
		desc = strings.ReplaceAll(desc, ":", " ")
		out = append(out, name+"\t"+desc)
	}
	return out
}

func installZshCompletion(home string) (scriptPath string, rcPath string, err error) {
	cfgDir := filepath.Join(home, ".config", "ai-audit")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		return "", "", err
	}
	scriptPath = filepath.Join(cfgDir, "completion.zsh")
	if err := os.WriteFile(scriptPath, []byte(generateZshCompletion()), 0o644); err != nil {
		return "", "", err
	}

	// Also drop standard autoload file into ~/.zsh/completions/_ai-audit for compinit discovery.
	compDir := filepath.Join(home, ".zsh", "completions")
	if os.MkdirAll(compDir, 0o755) == nil {
		_ = os.WriteFile(filepath.Join(compDir, "_ai-audit"), []byte("#compdef ai-audit\n"+generateZshCompletion()), 0o644)
	}

	rcPath = filepath.Join(home, ".zshrc")
	existing := ""
	if b, rerr := os.ReadFile(rcPath); rerr == nil {
		existing = string(b)
	}
	block := strings.Join([]string{
		completionMarkerStart,
		`[[ -f "$HOME/.config/ai-audit/completion.zsh" ]] && source "$HOME/.config/ai-audit/completion.zsh"`,
		completionMarkerEnd,
	}, "\n")

	updated := upsertMarkedBlock(existing, completionMarkerStart, completionMarkerEnd, block)
	if updated != existing {
		if err := os.WriteFile(rcPath, []byte(updated), 0o644); err != nil {
			return scriptPath, rcPath, err
		}
	}
	return scriptPath, rcPath, nil
}

func upsertMarkedBlock(content, startMarker, endMarker, block string) string {
	sIdx := strings.Index(content, startMarker)
	eIdx := strings.Index(content, endMarker)
	if sIdx >= 0 && eIdx > sIdx {
		endPos := eIdx + len(endMarker)
		return content[:sIdx] + block + content[endPos:]
	}
	if content == "" {
		return block + "\n"
	}
	sep := "\n"
	if !strings.HasSuffix(content, "\n") {
		sep = "\n\n"
	}
	return content + sep + block + "\n"
}

func generateZshCompletion() string {
	return `# Phone AI Audit (ai-audit / ai-tmux) Zsh Tab Completion
# Load dynamically via ~/.config/ai-audit/completion.zsh

_ai_audit_agy_profiles() {
  local -a profiles
  local line name desc
  while IFS=$'\t' read -r name desc; do
    [[ -n "$name" ]] && profiles+=("${name}:${desc}")
  done < <(ai-audit __complete agy-profiles 2>/dev/null)
  if (( ${#profiles[@]} > 0 )); then
    _describe -t agy-profiles '已保存 agy 账号' profiles
  fi
}

_ai_audit() {
  local -a top_cmds
  top_cmds=(
    'help:查看命令帮助（支持 ai-audit help <子命令>）'
    'status:查看中转状态、手机在线数、审批模式、tmux 会话'
    'usage:查看 Claude / Codex / Antigravity 用量与配额重置时间'
    'web:在浏览器打开本机 Agent 用量看板网页'
    'agy:管理多 Antigravity 账号并一键切换/新开终端'
    'clean:清理后台无窗口挂载的闲置 tmux 会话'
    'mode:切换审批模式 (auto / both / phone / notify)'
    'timeout:设置审批弹窗超时时长 (5-600 秒)'
    'auto-approve:开启/关闭超时自动确认授权 (on / off)'
    'awake:开启/关闭 Mac 防休眠 (on / off)'
    'terminal:开启/关闭远程交互终端总开关 (on / off)'
    'files:开启/关闭远程文件与 Git 总开关 (on / off)'
    'pair:打印 Android App 扫码配对二维码与配置信息'
    'test-msg:发送一条测试消息到手机'
    'test-approve:发起一次测试远程审批并等待决策'
    'completion:生成或安装 Shell Tab 自动补全脚本 (zsh / bash / install)'
  )

  if (( CURRENT == 2 )); then
    _describe -t commands 'ai-audit 命令' top_cmds
    return
  fi

  local cmd="${words[2]}"
  case "$cmd" in
    help)
      if (( CURRENT == 3 )); then
        _describe -t commands '查看子命令帮助' top_cmds
      fi
      ;;
    mode)
      if (( CURRENT == 3 )); then
        local -a modes
        modes=(
          'auto:智能离座（电脑前本地弹窗，离座或锁屏自动转交手机）'
          'both:双端同步（Mac 与手机同时弹起，先点为准、另一端秒消）'
          'phone:始终手机（全部审批请求发往手机处理）'
          'notify:仅通知不阻塞（只发推送提醒，不拦截终端）'
        )
        _describe -t modes '审批模式' modes
      fi
      ;;
    timeout)
      if (( CURRENT == 3 )); then
        local -a secs
        secs=(
          '15:15 秒'
          '30:30 秒（默认）'
          '60:60 秒'
          '120:120 秒'
          '300:300 秒'
        )
        _describe -t timeouts '超时秒数 (5-600)' secs
      fi
      ;;
    auto-approve|awake|terminal|files)
      if (( CURRENT == 3 )); then
        local -a switches
        switches=(
          'on:开启'
          'off:关闭'
        )
        _describe -t switches '开关' switches
      fi
      ;;
    completion)
      if (( CURRENT == 3 )); then
        local -a shells
        shells=(
          'install:自动安装并在 ~/.zshrc 启用 Zsh Tab 补全'
          'zsh:输出 Zsh 补全脚本'
          'bash:输出 Bash 补全脚本'
        )
        _describe -t shells '补全操作' shells
      fi
      ;;
    clean)
      local -a clean_flags
      clean_flags=(
        '--all:一并清理所有后台无窗口挂载的会话（含挂起的 AI 会话）'
        '--dry-run:仅预览待清理会话，不实际销毁'
      )
      _describe -t flags 'clean 选项' clean_flags
      ;;
    agy)
      local -a agy_cmds
      agy_cmds=(
        'ls:列出已保存的 agy 账号、到期状态与实时配额'
        'save:将当前全局已登录的 agy 账号保存到钥匙串档案'
        'login:清空槽位、弹出新终端登录新号并自动保存档案'
        'open:一键切换全局账号并弹出新的 tmux 终端运行 agy'
        'switch:仅切换全局默认 agy 账号（写入系统 Keychain）'
        'ttl:自定义账号登录到期/续登提醒周期'
        'rm:删除已保存的账号档案'
        'help:查看 agy 多账号管理完整帮助'
      )
      if (( CURRENT == 3 )); then
        _describe -t agy-commands 'agy 子命令' agy_cmds
        _ai_audit_agy_profiles
        return
      fi

      local sub="${words[3]}"
      local prev="${words[CURRENT-1]}"
      if [[ "$prev" == "--cwd" ]]; then
        _files -/
        return
      fi
      if [[ "$prev" == "--ttl" || "$prev" == "--expire" ]]; then
        local -a ttls
        ttls=('7d:7 天' '14d:14 天（默认提醒周期）' '30d:30 天' '24h:24 小时' 'auto:自动检测/默认14天' 'off:关闭提醒')
        _describe -t ttls '到期周期' ttls
        return
      fi

      case "$sub" in
        open|run)
          if (( CURRENT == 4 )); then
            _ai_audit_agy_profiles
          else
            local -a flags
            flags=(
              '--cwd:指定新终端工作目录'
              '--here:在当前终端直接运行 agy，不另弹新窗口'
              '--attach:已有同名 tmux 会话时直接切入'
              '--force:忽略过期拦截提示强制打开'
              '--no-wait:拉起终端后立即返回'
            )
            _describe -t flags 'open 选项' flags
          fi
          ;;
        switch|use)
          if (( CURRENT == 4 )); then
            _ai_audit_agy_profiles
          else
            local -a flags
            flags=('--force:忽略过期拦截提示强制切换')
            _describe -t flags 'switch 选项' flags
          fi
          ;;
        rm|delete|del)
          if (( CURRENT == 4 )); then
            _ai_audit_agy_profiles
          fi
          ;;
        ttl|expire)
          if (( CURRENT == 4 )); then
            _ai_audit_agy_profiles
          elif (( CURRENT == 5 )); then
            local -a ttls
            ttls=('7d:7 天' '14d:14 天（默认）' '30d:30 天' '24h:24 小时' 'auto:自动检测/默认14天' 'off:关闭提醒')
            _describe -t ttls '提醒周期' ttls
          fi
          ;;
        save|add)
          local -a flags
          flags=('--email:手动标注账号邮箱' '--ttl:自定义到期/续登提醒周期')
          _describe -t flags 'save 选项' flags
          ;;
        login)
          local -a flags
          flags=('--cwd:指定登录终端工作目录' '--ttl:自定义到期/续登提醒周期' '--force:当前登录态未备份时强制清空登录')
          _describe -t flags 'login 选项' flags
          ;;
        *)
          # Shorthand: ai-audit agy <alias> [open flags...]
          local -a flags
          flags=(
            '--cwd:指定新终端工作目录'
            '--here:在当前终端直接运行 agy，不另弹新窗口'
            '--attach:已有同名 tmux 会话时直接切入'
            '--force:忽略过期拦截提示强制打开'
            '--no-wait:拉起终端后立即返回'
          )
          _describe -t flags 'open 选项' flags
          ;;
      esac
      ;;
  esac
}

_ai_tmux() {
  if (( CURRENT == 2 )); then
    local -a tools
    tools=(
      'claude:在项目 tmux 会话中启动 Claude Code'
      'codex:在项目 tmux 会话中启动 OpenAI Codex'
      'agy:在项目 tmux 会话中启动 Antigravity'
    )
    _describe -t tools 'AI Agent 工具' tools
  fi
}

if [[ -n "${ZSH_VERSION:-}" ]]; then
  if (( ! $+functions[compdef] )); then
    autoload -Uz compinit && compinit -C
  fi
  compdef _ai_audit ai-audit
  compdef _ai_tmux ai-tmux
fi
`
}

func generateBashCompletion() string {
	return `# Phone AI Audit (ai-audit / ai-tmux) Bash Tab Completion
# Usage: eval "$(ai-audit completion bash)"

_ai_audit_bash() {
  local cur prev cmd sub
  COMPREPLY=()
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD-1]}"

  local top_cmds="help status usage web agy clean mode timeout auto-approve awake terminal files pair test-msg test-approve completion"
  if [[ ${COMP_CWORD} -eq 1 ]]; then
    COMPREPLY=( $(compgen -W "${top_cmds}" -- "${cur}") )
    return 0
  fi

  cmd="${COMP_WORDS[1]}"
  case "${cmd}" in
    help)
      COMPREPLY=( $(compgen -W "${top_cmds}" -- "${cur}") )
      ;;
    mode)
      COMPREPLY=( $(compgen -W "auto both phone notify" -- "${cur}") )
      ;;
    timeout)
      COMPREPLY=( $(compgen -W "15 30 60 120 300" -- "${cur}") )
      ;;
    auto-approve|awake|terminal|files)
      COMPREPLY=( $(compgen -W "on off" -- "${cur}") )
      ;;
    completion)
      COMPREPLY=( $(compgen -W "install zsh bash" -- "${cur}") )
      ;;
    clean)
      COMPREPLY=( $(compgen -W "--all --dry-run" -- "${cur}") )
      ;;
    agy)
      local profiles
      profiles="$(ai-audit __complete agy-profiles 2>/dev/null | cut -f1 | tr '\n' ' ')"
      if [[ ${COMP_CWORD} -eq 2 ]]; then
        COMPREPLY=( $(compgen -W "ls save login open switch ttl rm help ${profiles}" -- "${cur}") )
        return 0
      fi
      sub="${COMP_WORDS[2]}"
      if [[ "${prev}" == "--cwd" ]]; then
        COMPREPLY=( $(compgen -d -- "${cur}") )
        return 0
      fi
      if [[ "${prev}" == "--ttl" || "${prev}" == "--expire" ]]; then
        COMPREPLY=( $(compgen -W "7d 14d 30d 24h auto off" -- "${cur}") )
        return 0
      fi
      case "${sub}" in
        open|run)
          if [[ ${COMP_CWORD} -eq 3 ]]; then
            COMPREPLY=( $(compgen -W "${profiles}" -- "${cur}") )
          else
            COMPREPLY=( $(compgen -W "--cwd --here --attach --force --no-wait" -- "${cur}") )
          fi
          ;;
        switch|use)
          if [[ ${COMP_CWORD} -eq 3 ]]; then
            COMPREPLY=( $(compgen -W "${profiles}" -- "${cur}") )
          else
            COMPREPLY=( $(compgen -W "--force" -- "${cur}") )
          fi
          ;;
        rm|delete|del)
          COMPREPLY=( $(compgen -W "${profiles}" -- "${cur}") )
          ;;
        ttl|expire)
          if [[ ${COMP_CWORD} -eq 3 ]]; then
            COMPREPLY=( $(compgen -W "${profiles}" -- "${cur}") )
          else
            COMPREPLY=( $(compgen -W "7d 14d 30d 24h auto off" -- "${cur}") )
          fi
          ;;
        save|add)
          COMPREPLY=( $(compgen -W "--email --ttl" -- "${cur}") )
          ;;
        login)
          COMPREPLY=( $(compgen -W "--cwd --ttl --force" -- "${cur}") )
          ;;
        *)
          COMPREPLY=( $(compgen -W "--cwd --here --attach --force --no-wait" -- "${cur}") )
          ;;
      esac
      ;;
  esac
}

complete -F _ai_audit_bash ai-audit
complete -W "claude codex agy" ai-tmux
`
}
