import AppKit
import SwiftUI

// Tailcat 拨入和 Cloudflare 临时域名是两条连接方式。
// 这一段不读取 cloudflared 组件状态，缺组件时仍然可以配置。
struct TailcatAccessSection: View {
    @ObservedObject var model: ControlPanelModel
    @State private var portText = "80"
    @State private var allowText = ""
    @State private var address = ""
    @State private var errorText = ""
    @State private var revealed = false
    @State private var fieldsLoaded = false

    private var tailcatActive: Bool {
        (try? model.service.configuredTunnelMode()) == .tailcat
    }

    var body: some View {
        SettingsSection(L10n.text("Tailcat")) {
            VStack(alignment: .leading, spacing: 8) {
                Text(L10n.text("Run a Tailcat server on this computer. Copy the connection string and TCP port into the NexusDock node. NexusDock dials in. This does not publish a public MCP address."))
                    .font(.system(size: 12))
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                TextField(L10n.text("TCP port"), text: $portText)
                    .textFieldStyle(.roundedBorder)
                Text(L10n.text("Allowed clients"))
                    .font(.system(size: 13, weight: .medium))
                TextField(
                    L10n.text("Enter nodekey: followed by 64 hexadecimal digits, separated by commas or spaces. Leave blank to allow any client that has the connection string."),
                    text: $allowText,
                    axis: .vertical
                )
                .textFieldStyle(.roundedBorder)
                .lineLimit(2...4)
                HStack(spacing: 8) {
                    Button(L10n.text("Apply")) {
                        Task { await model.applyTailcat(portText: portText, allowText: allowText) }
                    }
                    .disabled(model.isBusy)
                    Button(L10n.text("Reset connection string")) {
                        revealed = false
                        address = ""
                        Task { await model.resetTailcatConnection() }
                    }
                    .disabled(model.isBusy || !tailcatActive)
                }
                Text(connectionDetail)
                    .font(.system(size: 11.5))
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
                HStack(spacing: 8) {
                    Button(revealed ? L10n.text("Hide") : L10n.text("Show")) {
                        revealed.toggle()
                    }
                    .disabled(address.isEmpty)
                    Button(L10n.text("Copy")) {
                        copyAddress()
                    }
                    .disabled(address.isEmpty)
                }
            }
            .padding(13)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .task(id: model.statusUpdatedAt) {
            refreshStatus()
        }
    }

    private var connectionDetail: String {
        if !errorText.isEmpty {
            return errorText
        }
        if address.isEmpty {
            return L10n.text("Waiting for the Tailcat connection string…")
        }
        if revealed {
            return address
        }
        return L10n.text("Copy this connection string and the TCP port into the NexusDock node. Treat the string as a password.")
    }

    private func refreshStatus() {
        let state = TailcatPanel.load(paths: model.service.paths)
        address = state.address
        errorText = state.error
        // 用户正在改端口和允许名单时，后台刷新不能把输入框盖回去。
        guard !fieldsLoaded else { return }
        portText = String(state.port)
        allowText = state.allowText
        fieldsLoaded = true
    }

    private func copyAddress() {
        guard !address.isEmpty else { return }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(address, forType: .string)
    }
}
