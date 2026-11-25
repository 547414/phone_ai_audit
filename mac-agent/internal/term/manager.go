package term

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"

	"phone-ai-audit/mac-agent/internal/proto"
)

type OutputFunc func(streamID [16]byte, data []byte)
type ClosedFunc func(streamIDHex string, reason string)

type session struct {
	idHex string
	idRaw [16]byte
	ptmx  *os.File
	cmd   *exec.Cmd
	once  sync.Once
}

func (s *session) close() {
	s.once.Do(func() {
		if s.ptmx != nil {
			_ = s.ptmx.Close()
		}
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
	})
}

type Manager struct {
	onOutput OutputFunc
	onClosed ClosedFunc

	mu       sync.Mutex
	enabled  bool
	sessions map[[16]byte]*session
}

func NewManager(onOutput OutputFunc, onClosed ClosedFunc) *Manager {
	return &Manager{
		onOutput: onOutput,
		onClosed: onClosed,
		enabled:  true,
		sessions: make(map[[16]byte]*session),
	}
}

func (m *Manager) SetEnabled(v bool) {
	m.mu.Lock()
	m.enabled = v
	var toClose []*session
	if !v {
		for k, s := range m.sessions {
			toClose = append(toClose, s)
			delete(m.sessions, k)
		}
	}
	m.mu.Unlock()
	for _, s := range toClose {
		s.close()
		if m.onClosed != nil {
			m.onClosed(s.idHex, "terminal_disabled")
		}
	}
}

func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled
}

func FindTmux() string {
	candidates := []string{
		"/opt/homebrew/bin/tmux",
		"/usr/local/bin/tmux",
		"/usr/bin/tmux",
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("tmux"); err == nil {
		return p
	}
	return ""
}

func enrichedEnv() []string {
	home, _ := os.UserHomeDir()
	extraPath := strings.Join([]string{
		filepath.Join(home, ".local", "bin"),
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
		"/usr/sbin",
		"/sbin",
	}, ":")
	env := os.Environ()
	out := make([]string, 0, len(env)+4)
	hasPath := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			out = append(out, "PATH="+extraPath+":"+strings.TrimPrefix(kv, "PATH="))
			hasPath = true
		} else if strings.HasPrefix(kv, "TERM=") || strings.HasPrefix(kv, "TMUX=") {
			continue
		} else {
			out = append(out, kv)
		}
	}
	if !hasPath {
		out = append(out, "PATH="+extraPath)
	}
	out = append(out, "TERM=xterm-256color", "LANG=zh_CN.UTF-8", "LC_ALL=zh_CN.UTF-8")
	return out
}

func CurrentTmuxSession() string {
	if os.Getenv("TMUX") == "" {
		return ""
	}
	tmuxBin := FindTmux()
	if tmuxBin == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, tmuxBin, "display-message", "-p", "#S").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func findTmuxSocket() string {
	uid := os.Getuid()
	candidates := []string{
		fmt.Sprintf("/private/tmp/tmux-%d/default", uid),
		fmt.Sprintf("/tmp/tmux-%d/default", uid),
	}
	if td := strings.TrimRight(os.Getenv("TMUX_TMPDIR"), "/"); td != "" {
		candidates = append([]string{fmt.Sprintf("%s/tmux-%d/default", td, uid)}, candidates...)
	}
	if td := strings.TrimRight(os.Getenv("TMPDIR"), "/"); td != "" {
		candidates = append(candidates, fmt.Sprintf("%s/tmux-%d/default", td, uid))
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && (fi.Mode()&os.ModeSocket) != 0 {
			return p
		}
	}
	return ""
}

func ListTmuxSessions() []proto.TmuxSession {
	tmuxBin := FindTmux()
	if tmuxBin == "" {
		return []proto.TmuxSession{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Use ASCII unit separator (0x1f) so empty pane titles or spaces never corrupt field splitting.
	const sep = "\x1f"
	format := strings.Join([]string{
		"#{session_name}",
		"#{session_windows}",
		"#{session_attached}",
		"#{session_activity}",
		"#{pane_current_command}",
		"#{pane_current_path}",
		"#{pane_pid}",
		"#{pane_title}",
	}, sep)

	args := []string{}
	if sock := findTmuxSocket(); sock != "" {
		args = append(args, "-S", sock)
	}
	args = append(args, "list-panes", "-a", "-F", format)

	cmd := exec.CommandContext(ctx, tmuxBin, args...)
	cmd.Env = enrichedEnv()
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		errTxt := strings.TrimSpace(stderr.String())
		if errTxt != "" && !strings.Contains(errTxt, "no server running") && !strings.Contains(errTxt, "No such file") {
			log.Printf("ListTmuxSessions error: %v (%s)", err, errTxt)
		}
		return []proto.TmuxSession{}
	}

	seen := make(map[string]*proto.TmuxSession)
	order := make([]string, 0)
	for _, line := range strings.Split(strings.TrimRight(string(out), "\r\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, sep)
		if len(parts) < 8 {
			continue
		}
		name := parts[0]
		wins, _ := strconv.Atoi(parts[1])
		attached := parts[2] != "0"
		actSec, _ := strconv.ParseInt(parts[3], 10, 64)
		curCmd := parts[4]
		cwd := parts[5]
		panePid := parts[6]
		paneTitle := parts[7]

		agent := detectAgent(name, curCmd, paneTitle, panePid)
		proj := filepath.Base(cwd)
		if proj == "." || proj == "/" {
			proj = ""
		}

		if existing, ok := seen[name]; ok {
			// Prefer a pane running an AI agent over a plain shell pane.
			if existing.Agent == "" && agent != "" {
				existing.Agent = agent
				existing.Command = curCmd
				existing.Cwd = cwd
				existing.ProjectName = proj
			}
			continue
		}
		s := &proto.TmuxSession{
			Name:        name,
			Windows:     wins,
			Attached:    attached,
			Command:     curCmd,
			Agent:       agent,
			Cwd:         cwd,
			ProjectName: proj,
			ActivityAt:  actSec * 1000,
		}
		seen[name] = s
		order = append(order, name)
	}

	res := make([]proto.TmuxSession, 0, len(order))
	for _, name := range order {
		res = append(res, *seen[name])
	}
	return res
}

func detectAgent(sessionName, curCmd, paneTitle, panePid string) string {
	combined := strings.ToLower(sessionName + " " + curCmd + " " + paneTitle)
	if strings.Contains(combined, "claude") {
		return "claude"
	}
	if strings.Contains(combined, "codex") {
		return "codex"
	}
	if strings.Contains(combined, "agy") || strings.Contains(combined, "antigravity") {
		return "agy"
	}
	if panePid != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "/usr/bin/pgrep", "-P", panePid, "-l").Output(); err == nil {
			child := strings.ToLower(string(out))
			if strings.Contains(child, "claude") {
				return "claude"
			}
			if strings.Contains(child, "codex") {
				return "codex"
			}
			if strings.Contains(child, "agy") {
				return "agy"
			}
		}
	}
	return ""
}

func queryTmuxWindowSize(tmuxBin, target string) (int, int) {
	if tmuxBin == "" || target == "" {
		return 0, 0
	}
	args := []string{}
	if sock := findTmuxSocket(); sock != "" {
		args = append(args, "-S", sock)
	}
	args = append(args, "display-message", "-p", "-t", target, "#{window_width} #{window_height}")
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, tmuxBin, args...)
	cmd.Env = enrichedEnv()
	out, err := cmd.Output()
	if err != nil {
		return 0, 0
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) != 2 {
		return 0, 0
	}
	w, err1 := strconv.Atoi(parts[0])
	h, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || w < 20 || h < 8 {
		return 0, 0
	}
	return w, h
}

func (m *Manager) Open(streamIDHex, mode, target string, cols, rows int) (int, int, error) {
	if !m.Enabled() {
		return 0, 0, errors.New("远程终端已在设置中关闭")
	}
	raw, err := hex.DecodeString(streamIDHex)
	if err != nil || len(raw) != 16 {
		return 0, 0, errors.New("invalid stream_id")
	}
	var sid [16]byte
	copy(sid[:], raw)

	if cols <= 0 {
		cols = 135
	}
	if rows <= 0 {
		rows = 35
	}

	home, _ := os.UserHomeDir()
	tmuxBin := FindTmux()
	var cmd *exec.Cmd
	notifyLabel := "新建终端"
	openMacGUISession := ""

	switch mode {
	case "tmux_attach":
		if tmuxBin == "" {
			return 0, 0, errors.New("Mac 未安装 tmux")
		}
		if target == "" {
			return 0, 0, errors.New("未指定 tmux 会话名称")
		}
		// Match Mac's existing tmux window dimensions so Mac never shrinks or shows dotted padding.
		if w, h := queryTmuxWindowSize(tmuxBin, target); w > 0 && h > 0 {
			cols, rows = w, h
		}
		notifyLabel = "tmux: " + target
		if sock := findTmuxSocket(); sock != "" {
			cmd = exec.Command(tmuxBin, "-S", sock, "attach-session", "-t", target)
		} else {
			cmd = exec.Command(tmuxBin, "attach-session", "-t", target)
		}
	default:
		if tmuxBin != "" {
			sessName := target
			if sessName == "" {
				sessName = "phone-" + streamIDHex[:6]
			}
			// Default new Mac sessions to desktop size (135x35) so Mac GUI Terminal matches cleanly with zero dots.
			if cols < 120 {
				cols = 135
			}
			if rows < 30 {
				rows = 35
			}
			notifyLabel = "新建 tmux: " + sessName
			openMacGUISession = sessName
			if sock := findTmuxSocket(); sock != "" {
				cmd = exec.Command(tmuxBin, "-S", sock, "new-session", "-A", "-s", sessName)
			} else {
				cmd = exec.Command(tmuxBin, "new-session", "-A", "-s", sessName)
			}
		} else {
			cmd = exec.Command("/bin/zsh", "-l")
		}
	}
	cmd.Dir = home
	cmd.Env = enrichedEnv()

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: uint16(rows),
		Cols: uint16(cols),
	})
	if err != nil {
		return 0, 0, fmt.Errorf("start pty: %w", err)
	}

	sess := &session{
		idHex: streamIDHex,
		idRaw: sid,
		ptmx:  ptmx,
		cmd:   cmd,
	}

	m.mu.Lock()
	if prev := m.sessions[sid]; prev != nil {
		prev.close()
	}
	m.sessions[sid] = sess
	m.mu.Unlock()

	go notifyMacTerminalAttached(notifyLabel)
	if openMacGUISession != "" {
		go openMacTerminalWindow(tmuxBin, openMacGUISession)
	}

	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 && m.onOutput != nil {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				m.onOutput(sid, chunk)
			}
			if err != nil {
				reason := "process_exited"
				if !errors.Is(err, io.EOF) {
					reason = err.Error()
				}
				m.Close(streamIDHex, reason, true)
				return
			}
		}
	}()

	return cols, rows, nil
}

func (m *Manager) WriteStdin(sid [16]byte, data []byte) {
	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess == nil || len(data) == 0 {
		return
	}
	_, _ = sess.ptmx.Write(data)
}

func (m *Manager) Resize(streamIDHex string, cols, rows int) {
	raw, err := hex.DecodeString(streamIDHex)
	if err != nil || len(raw) != 16 {
		return
	}
	var sid [16]byte
	copy(sid[:], raw)
	m.mu.Lock()
	sess := m.sessions[sid]
	m.mu.Unlock()
	if sess == nil || cols <= 0 || rows <= 0 {
		return
	}
	_ = pty.Setsize(sess.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

func (m *Manager) Close(streamIDHex, reason string, notifyRelay bool) {
	raw, err := hex.DecodeString(streamIDHex)
	if err != nil || len(raw) != 16 {
		return
	}
	var sid [16]byte
	copy(sid[:], raw)

	m.mu.Lock()
	sess := m.sessions[sid]
	delete(m.sessions, sid)
	m.mu.Unlock()
	if sess == nil {
		return
	}
	sess.close()
	if notifyRelay && m.onClosed != nil {
		m.onClosed(streamIDHex, reason)
	}
}

func (m *Manager) CloseAll() {
	m.mu.Lock()
	all := make([]*session, 0, len(m.sessions))
	for k, s := range m.sessions {
		all = append(all, s)
		delete(m.sessions, k)
	}
	m.mu.Unlock()
	for _, s := range all {
		s.close()
	}
}

func (m *Manager) KillSession(target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return errors.New("empty tmux target")
	}
	tmuxBin := FindTmux()
	if tmuxBin == "" {
		return errors.New("tmux not installed")
	}
	baseArgs := []string{}
	if sock := findTmuxSocket(); sock != "" {
		baseArgs = append(baseArgs, "-S", sock)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Collect attached client TTYs and pane PIDs BEFORE killing the tmux session
	var clientTTYs []string
	{
		args := append(append([]string{}, baseArgs...), "list-clients", "-t", target, "-F", "#{client_tty}")
		c := exec.CommandContext(ctx, tmuxBin, args...)
		c.Env = enrichedEnv()
		if out, err := c.Output(); err == nil {
			for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				ln = strings.TrimSpace(ln)
				if ln != "" {
					clientTTYs = append(clientTTYs, ln)
				}
			}
		}
	}
	var panePIDs []string
	{
		args := append(append([]string{}, baseArgs...), "list-panes", "-t", target, "-F", "#{pane_pid}")
		c := exec.CommandContext(ctx, tmuxBin, args...)
		c.Env = enrichedEnv()
		if out, err := c.Output(); err == nil {
			for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				ln = strings.TrimSpace(ln)
				if ln != "" {
					panePIDs = append(panePIDs, ln)
				}
			}
		}
	}

	// 2. Gracefully terminate child processes inside each pane so agents/shells don't linger
	for _, pid := range panePIDs {
		_ = exec.CommandContext(ctx, "/usr/bin/pkill", "-TERM", "-P", pid).Run()
	}
	if len(panePIDs) > 0 {
		time.Sleep(150 * time.Millisecond)
		for _, pid := range panePIDs {
			_ = exec.CommandContext(ctx, "/usr/bin/pkill", "-KILL", "-P", pid).Run()
		}
	}

	// 3. Kill the tmux session itself
	killArgs := append(append([]string{}, baseArgs...), "kill-session", "-t", target)
	cmd := exec.CommandContext(ctx, tmuxBin, killArgs...)
	cmd.Env = enrichedEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		errTxt := strings.TrimSpace(string(out))
		// Even if tmux already exited, still attempt to close any leftover Mac Terminal.app window
		go closeMacTerminalWindows(target, clientTTYs)
		if !strings.Contains(errTxt, "can't find session") && !strings.Contains(errTxt, "no server running") {
			log.Printf("KillSession(%s) error: %v (%s)", target, err, errTxt)
			return err
		}
		return nil
	}

	// 4. Remove any leftover launcher script and close the Mac Terminal.app GUI window(s)
	_ = os.Remove(filepath.Join(os.TempDir(), fmt.Sprintf("ai-audit-open-%s.command", target)))
	go closeMacTerminalWindows(target, clientTTYs)
	return nil
}

func closeMacTerminalWindows(target string, clientTTYs []string) {
	// Wait briefly so Terminal.app reflects the exited process state
	time.Sleep(250 * time.Millisecond)

	esc := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		return strings.ReplaceAll(s, `"`, `\"`)
	}
	ttyItems := make([]string, 0, len(clientTTYs))
	for _, tty := range clientTTYs {
		if strings.HasPrefix(tty, "/dev/") {
			ttyItems = append(ttyItems, `"`+esc(tty)+`"`)
		}
	}
	ttyListLiteral := "{" + strings.Join(ttyItems, ", ") + "}"
	safeTarget := esc(target)

	script := fmt.Sprintf(`
tell application "System Events"
	if not (exists process "Terminal") then return
end tell
tell application "Terminal"
	set targetTTYs to %s
	set targetName to "%s"
	set winList to every window
	repeat with i from (count of winList) to 1 by -1
		try
			set w to item i of winList
			set shouldClose to false
			if targetName is not "" and (name of w contains targetName) then
				set shouldClose to true
			else
				repeat with t in (every tab of w)
					try
						if (tty of t) is in targetTTYs then
							set shouldClose to true
							exit repeat
						end if
						if targetName is not "" and (custom title of t contains targetName) then
							set shouldClose to true
							exit repeat
						end if
					end try
				end repeat
			end if
			if shouldClose then
				close w saving no
			end if
		end try
	end repeat
end tell
`, ttyListLiteral, safeTarget)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script).Run()
}

func notifyMacTerminalAttached(label string) {
	safe := strings.ReplaceAll(label, `"`, `'`)
	script := fmt.Sprintf(`display notification "手机端已连接终端 (%s)" with title "Phone AI Audit"`, safe)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script).Run()
}

// openMacTerminalWindow opens a real graphical Terminal.app window attached to the newly created tmux session.
// Uses LaunchServices `open -a Terminal <.command>` to avoid macOS AppleScript Automation permission prompts,
// and self-closes its Terminal.app window cleanly once the tmux session exits or is killed from phone.
func openMacTerminalWindow(tmuxBin, sessName string) {
	time.Sleep(300 * time.Millisecond)

	sock := findTmuxSocket()
	attachCmd := fmt.Sprintf("%q attach-session -t %q", tmuxBin, sessName)
	if sock != "" {
		attachCmd = fmt.Sprintf("%q -S %q attach-session -t %q", tmuxBin, sock, sessName)
	}

	scriptPath := filepath.Join(os.TempDir(), fmt.Sprintf("ai-audit-open-%s.command", sessName))
	content := fmt.Sprintf(`#!/bin/zsh
export TERM=xterm-256color
CUR_TTY="$(tty 2>/dev/null || true)"
rm -f %q
%s
if [[ -n "$CUR_TTY" ]]; then
  (/usr/bin/osascript -e 'tell application "Terminal" to close (every window whose tty of selected tab is "'"$CUR_TTY"'") saving no' >/dev/null 2>&1 &)
fi
exit 0
`, scriptPath, attachCmd)
	if err := os.WriteFile(scriptPath, []byte(content), 0o700); err != nil {
		log.Printf("write open command script failed: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "/usr/bin/open", "-a", "Terminal", scriptPath).Run(); err != nil {
		log.Printf("open Terminal.app failed: %v", err)
		return
	}
}
