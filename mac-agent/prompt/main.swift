import AppKit
import Foundation

struct PromptInput: Decodable {
    let agent: String?
    let project_name: String?
    let task_title: String?
    let tool_name: String?
    let summary: String?
    let risk_level: String?
    let risk_reasons: [String]?
    let timeout_sec: Int?
    let slot: Int?
}

struct PromptOutput: Encodable {
    let decision: String
    let reason: String
}

final class FloatingApprovalPanel: NSPanel {
    override var canBecomeKey: Bool { true }
    override var canBecomeMain: Bool { false }
}

final class PromptController: NSObject, NSTextFieldDelegate {
    private let input: PromptInput
    private var panel: FloatingApprovalPanel!
    private var reasonField: NSTextField!
    private var timerLabel: NSTextField!
    private var remainingSec: Int
    private var countdownTimer: Timer?
    private var finished = false

    init(input: PromptInput) {
        self.input = input
        self.remainingSec = max(5, input.timeout_sec ?? 180)
        super.init()
    }

    func show() {
        let width: CGFloat = 400
        let hasTaskTitle = !(input.task_title ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        let hasReasons = !(input.risk_reasons ?? []).isEmpty
        var height: CGFloat = 228
        if hasTaskTitle { height += 20 }
        if hasReasons { height += 22 }

        let screen = screenWithCursor() ?? NSScreen.main ?? NSScreen.screens.first
        let vf = screen?.visibleFrame ?? NSRect(x: 0, y: 0, width: 1280, height: 800)
        let slot = max(0, (input.slot ?? 0) % 4)
        let margin: CGFloat = 18
        let gap: CGFloat = 12
        let x = vf.maxX - width - margin
        let y = max(vf.minY + margin, vf.maxY - height - margin - CGFloat(slot) * (height + gap))

        panel = FloatingApprovalPanel(
            contentRect: NSRect(x: x, y: y, width: width, height: height),
            styleMask: [.borderless, .nonactivatingPanel],
            backing: .buffered,
            defer: false
        )
        panel.level = .floating
        panel.isOpaque = false
        panel.backgroundColor = .clear
        panel.hasShadow = true
        panel.isMovableByWindowBackground = true
        panel.becomesKeyOnlyIfNeeded = true
        panel.hidesOnDeactivate = false
        panel.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .ignoresCycle]

        let root = NSVisualEffectView(frame: NSRect(x: 0, y: 0, width: width, height: height))
        root.material = .popover
        root.blendingMode = .behindWindow
        root.state = .active
        root.wantsLayer = true
        root.layer?.cornerRadius = 14
        root.layer?.masksToBounds = true
        root.layer?.borderWidth = isHighRisk ? 1.5 : 0.75
        root.layer?.borderColor = (isHighRisk ? NSColor.systemRed.withAlphaComponent(0.75) : NSColor.separatorColor.withAlphaComponent(0.6)).cgColor

        panel.contentView = root
        buildLayout(in: root, width: width, height: height, hasTaskTitle: hasTaskTitle, hasReasons: hasReasons)

        panel.orderFrontRegardless()
        playSubtleSound()

        countdownTimer = Timer.scheduledTimer(withTimeInterval: 1.0, repeats: true) { [weak self] _ in
            guard let self = self else { return }
            self.remainingSec -= 1
            if self.remainingSec <= 0 {
                self.finish(decision: "fallback", reason: "电脑浮窗等待超时")
                return
            }
            self.timerLabel.stringValue = "\(self.remainingSec)s"
            if self.remainingSec <= 25 {
                self.timerLabel.textColor = .systemRed
            }
        }
    }

    private var isHighRisk: Bool {
        (input.risk_level ?? "").lowercased() == "high"
    }

    private func buildLayout(in root: NSView, width: CGFloat, height: CGFloat, hasTaskTitle: Bool, hasReasons: Bool) {
        let pad: CGFloat = 14
        let innerW = width - pad * 2
        var curY = height - pad

        // 1. Header row (Agent badge + High-Risk badge + Project + Countdown + Close)
        curY -= 22
        var curX = pad

        let agentBadge = makeBadge(text: agentDisplayName(input.agent ?? ""), color: agentColor(input.agent ?? ""))
        agentBadge.frame.origin = NSPoint(x: curX, y: curY + 1)
        root.addSubview(agentBadge)
        curX += agentBadge.frame.width + 6

        if isHighRisk {
            let riskBadge = makeBadge(text: "高危", color: .systemRed)
            riskBadge.frame.origin = NSPoint(x: curX, y: curY + 1)
            root.addSubview(riskBadge)
            curX += riskBadge.frame.width + 6
        }

        let closeBtn = NSButton(title: "✕", target: self, action: #selector(onFallback))
        closeBtn.isBordered = false
        closeBtn.font = NSFont.systemFont(ofSize: 12, weight: .medium)
        closeBtn.contentTintColor = .secondaryLabelColor
        closeBtn.frame = NSRect(x: width - pad - 20, y: curY, width: 20, height: 22)
        root.addSubview(closeBtn)

        timerLabel = NSTextField(labelWithString: "\(remainingSec)s")
        timerLabel.font = NSFont.monospacedDigitSystemFont(ofSize: 12, weight: .semibold)
        timerLabel.textColor = isHighRisk ? .systemRed : .secondaryLabelColor
        timerLabel.alignment = .right
        timerLabel.frame = NSRect(x: width - pad - 20 - 44, y: curY + 2, width: 40, height: 18)
        root.addSubview(timerLabel)

        let project = (input.project_name ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        let titleText = project.isEmpty ? "请求权限审批" : "\(project) · 审批"
        let titleLabel = NSTextField(labelWithString: titleText)
        titleLabel.font = NSFont.systemFont(ofSize: 13, weight: .semibold)
        titleLabel.textColor = .labelColor
        titleLabel.lineBreakMode = .byTruncatingTail
        let titleW = max(60, (width - pad - 68) - curX)
        titleLabel.frame = NSRect(x: curX, y: curY + 2, width: titleW, height: 18)
        root.addSubview(titleLabel)

        // 2. Optional task title
        if hasTaskTitle {
            curY -= 20
            let taskLabel = NSTextField(labelWithString: (input.task_title ?? "").trimmingCharacters(in: .whitespacesAndNewlines))
            taskLabel.font = NSFont.systemFont(ofSize: 11.5)
            taskLabel.textColor = .secondaryLabelColor
            taskLabel.lineBreakMode = .byTruncatingTail
            taskLabel.frame = NSRect(x: pad, y: curY + 1, width: innerW, height: 16)
            root.addSubview(taskLabel)
        }

        // 3. Monospace scrollable command / summary box
        curY -= 10
        let boxH: CGFloat = 76
        curY -= boxH
        let boxContainer = NSView(frame: NSRect(x: pad, y: curY, width: innerW, height: boxH))
        boxContainer.wantsLayer = true
        boxContainer.layer?.cornerRadius = 9
        boxContainer.layer?.backgroundColor = NSColor.textBackgroundColor.withAlphaComponent(0.55).cgColor
        boxContainer.layer?.borderWidth = 0.5
        boxContainer.layer?.borderColor = NSColor.separatorColor.withAlphaComponent(0.5).cgColor

        let scroll = NSScrollView(frame: NSRect(x: 6, y: 5, width: innerW - 12, height: boxH - 10))
        scroll.drawsBackground = false
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        scroll.borderType = .noBorder

        let textView = NSTextView(frame: NSRect(x: 0, y: 0, width: innerW - 16, height: boxH - 10))
        textView.drawsBackground = false
        textView.isEditable = false
        textView.isSelectable = true
        textView.font = NSFont.monospacedSystemFont(ofSize: 12, weight: .regular)
        textView.textColor = .labelColor
        textView.textContainerInset = NSSize(width: 2, height: 2)
        textView.string = (input.summary ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        scroll.documentView = textView
        boxContainer.addSubview(scroll)
        root.addSubview(boxContainer)

        // 4. Optional risk reasons
        if hasReasons {
            curY -= 22
            let reasonsText = "⚠ " + (input.risk_reasons ?? []).joined(separator: " · ")
            let rLabel = NSTextField(labelWithString: reasonsText)
            rLabel.font = NSFont.systemFont(ofSize: 11.5, weight: .medium)
            rLabel.textColor = isHighRisk ? .systemRed : .systemOrange
            rLabel.lineBreakMode = .byTruncatingTail
            rLabel.frame = NSRect(x: pad, y: curY + 2, width: innerW, height: 16)
            root.addSubview(rLabel)
        }

        // 5. Optional deny reason input
        curY -= 10
        let inputH: CGFloat = 26
        curY -= inputH
        reasonField = NSTextField(frame: NSRect(x: pad, y: curY, width: innerW, height: inputH))
        reasonField.placeholderString = "可选：输入拒绝理由后点「拒绝」（或按回车拒绝）"
        reasonField.font = NSFont.systemFont(ofSize: 12)
        reasonField.isBezeled = true
        reasonField.bezelStyle = .roundedBezel
        reasonField.focusRingType = .none
        reasonField.delegate = self
        root.addSubview(reasonField)

        // 6. Action buttons row: 允许 | 拒绝 | 退回终端
        curY -= 12
        let btnH: CGFloat = 32
        curY -= btnH
        let spacing: CGFloat = 8
        let btnW = (innerW - spacing * 2) / 3.0

        let allowBtn = makeActionButton(
            title: isHighRisk ? "确认允许" : "允许",
            bg: NSColor.systemGreen,
            fg: .white,
            frame: NSRect(x: pad, y: curY, width: btnW, height: btnH),
            action: #selector(onApprove)
        )
        let denyBtn = makeActionButton(
            title: "拒绝",
            bg: NSColor.systemRed,
            fg: .white,
            frame: NSRect(x: pad + btnW + spacing, y: curY, width: btnW, height: btnH),
            action: #selector(onDeny)
        )
        let fbBtn = makeActionButton(
            title: "退回终端",
            bg: NSColor.controlColor,
            fg: .labelColor,
            frame: NSRect(x: pad + (btnW + spacing) * 2, y: curY, width: btnW, height: btnH),
            action: #selector(onFallback)
        )

        root.addSubview(allowBtn)
        root.addSubview(denyBtn)
        root.addSubview(fbBtn)
    }

    private func makeBadge(text: String, color: NSColor) -> NSView {
        let label = NSTextField(labelWithString: text)
        label.font = NSFont.systemFont(ofSize: 10.5, weight: .semibold)
        label.textColor = .white
        label.sizeToFit()
        let w = label.frame.width + 12
        let h: CGFloat = 19
        let box = NSView(frame: NSRect(x: 0, y: 0, width: w, height: h))
        box.wantsLayer = true
        box.layer?.cornerRadius = 6
        box.layer?.backgroundColor = color.cgColor
        label.frame = NSRect(x: 6, y: 2, width: label.frame.width, height: 15)
        box.addSubview(label)
        return box
    }

    private func makeActionButton(title: String, bg: NSColor, fg: NSColor, frame: NSRect, action: Selector) -> NSButton {
        let btn = NSButton(frame: frame)
        btn.isBordered = false
        btn.wantsLayer = true
        btn.layer?.cornerRadius = 8
        btn.layer?.backgroundColor = bg.cgColor
        btn.target = self
        btn.action = action
        let attr = NSAttributedString(
            string: title,
            attributes: [
                .foregroundColor: fg,
                .font: NSFont.systemFont(ofSize: 13, weight: .semibold)
            ]
        )
        btn.attributedTitle = attr
        return btn
    }

    private func agentDisplayName(_ agent: String) -> String {
        switch agent.lowercased() {
        case "claude": return "Claude Code"
        case "codex": return "Codex"
        case "agy": return "Antigravity"
        default: return agent.isEmpty ? "AI" : agent
        }
    }

    private func agentColor(_ agent: String) -> NSColor {
        switch agent.lowercased() {
        case "claude": return NSColor(calibratedRed: 0.85, green: 0.47, blue: 0.02, alpha: 1.0)
        case "codex": return NSColor(calibratedRed: 0.06, green: 0.68, blue: 0.49, alpha: 1.0)
        case "agy": return NSColor.systemBlue
        default: return NSColor.systemGray
        }
    }

    private func screenWithCursor() -> NSScreen? {
        let loc = NSEvent.mouseLocation
        return NSScreen.screens.first(where: { NSMouseInRect(loc, $0.frame, false) })
    }

    private func playSubtleSound() {
        let name = isHighRisk ? "Basso" : "Tink"
        NSSound(named: NSSound.Name(name))?.play()
    }

    func control(_ control: NSControl, textView: NSTextView, doCommandBy commandSelector: Selector) -> Bool {
        if commandSelector == #selector(NSResponder.insertNewline(_:)) {
            onDeny()
            return true
        }
        if commandSelector == #selector(NSResponder.cancelOperation(_:)) {
            onFallback()
            return true
        }
        return false
    }

    @objc private func onApprove() {
        finish(decision: "approved", reason: "")
    }

    @objc private func onDeny() {
        let r = reasonField.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
        finish(decision: "denied", reason: r)
    }

    @objc private func onFallback() {
        finish(decision: "fallback", reason: "电脑端选择退回终端处理")
    }

    private func finish(decision: String, reason: String) {
        guard !finished else { return }
        finished = true
        countdownTimer?.invalidate()
        panel?.orderOut(nil)

        let out = PromptOutput(decision: decision, reason: reason)
        if let data = try? JSONEncoder().encode(out), let str = String(data: data, encoding: .utf8) {
            FileHandle.standardOutput.write((str + "\n").data(using: .utf8)!)
        }
        NSApplication.shared.terminate(nil)
    }
}

// Install clean signal handlers so daemon cancellation dismisses the floating window instantaneously.
signal(SIGTERM) { _ in _exit(0) }
signal(SIGINT) { _ in _exit(0) }

let rawInput = FileHandle.standardInput.readDataToEndOfFile()
let parsedInput: PromptInput
if let decoded = try? JSONDecoder().decode(PromptInput.self, from: rawInput) {
    parsedInput = decoded
} else {
    parsedInput = PromptInput(
        agent: "ai",
        project_name: nil,
        task_title: nil,
        tool_name: nil,
        summary: "请求执行操作",
        risk_level: "low",
        risk_reasons: nil,
        timeout_sec: 180,
        slot: 0
    )
}

let app = NSApplication.shared
app.setActivationPolicy(.accessory)

let controller = PromptController(input: parsedInput)
DispatchQueue.main.async {
    controller.show()
}
app.run()
