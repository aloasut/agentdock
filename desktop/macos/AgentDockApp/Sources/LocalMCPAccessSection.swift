import AppKit
import SwiftUI

// 局域网 MCP 改的是 AGENTDOCK_HOST，Tailcat MCP 改的是隧道模式。
// 两个按钮各自开关，启动一个不会把另一个关掉。
struct LocalMCPAccessSection: View {
    @ObservedObject var model: ControlPanelModel
    @State private var tailcatAddress = ""

    private var configuration: ServiceConfiguration? { model.status.configuration }
    private var lanOn: Bool { configuration?.isLANListen == true }
    private var tailcatOn: Bool {
        (try? model.service.configuredTunnelMode()) == .tailcat
    }

    var body: some View {
        SettingsSection(L10n.text("Local MCP")) {
            VStack(alignment: .leading, spacing: 0) {
                accessRow(
                    title: L10n.text("LAN MCP"),
                    detail: lanOn
                        ? L10n.text("On. Other devices on this network can use the addresses below. The Bearer token is still required.")
                        : L10n.text("Off. Only this Mac can reach MCP."),
                    buttonTitle: lanOn ? L10n.text("Stop LAN MCP") : L10n.text("Start LAN MCP")
                ) {
                    Task { await model.setLANListen(enabled: !lanOn) }
                }
                if lanOn {
                    addressList
                }
                RowDivider()
                accessRow(
                    title: L10n.text("Tailcat MCP"),
                    detail: tailcatOn
                        ? L10n.text("On. Copy the connection string into the client. LAN MCP can stay on.")
                        : L10n.text("Off. One click starts a Tailcat server on the saved port, or port 80. LAN MCP can stay on."),
                    buttonTitle: tailcatOn ? L10n.text("Stop Tailcat MCP") : L10n.text("Start Tailcat MCP")
                ) {
                    Task { await model.setTailcatServer(enabled: !tailcatOn) }
                }
                if tailcatOn {
                    HStack(spacing: 8) {
                        Button(L10n.text("Copy connection string")) {
                            copy(tailcatAddress)
                        }
                        .controlSize(.small)
                        .disabled(tailcatAddress.isEmpty)
                        Text(tailcatAddress.isEmpty
                             ? L10n.text("Waiting for the Tailcat connection string…")
                             : L10n.text("Copy this connection string and the TCP port into the NexusDock node. Treat the string as a password."))
                            .font(.system(size: 11.5))
                            .foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    .padding(.horizontal, 13)
                    .padding(.bottom, 12)
                }
            }
        }
        .task(id: model.statusUpdatedAt) {
            tailcatAddress = TailcatPanel.load(paths: model.service.paths).address
        }
    }

    @ViewBuilder
    private var addressList: some View {
        VStack(alignment: .leading, spacing: 6) {
            if let local = configuration?.localMCPURL?.absoluteString {
                addressRow(local)
            }
            let lanURLs = configuration?.lanMCPURLs ?? []
            if lanURLs.isEmpty {
                Text(L10n.text("No private network address is available yet. Restart after this Mac joins a network."))
                    .font(.system(size: 11.5))
                    .foregroundStyle(.secondary)
            } else {
                ForEach(lanURLs, id: \.absoluteString) { url in
                    addressRow(url.absoluteString)
                }
            }
        }
        .padding(.horizontal, 13)
        .padding(.bottom, 12)
    }

    private func addressRow(_ value: String) -> some View {
        HStack(spacing: 8) {
            Text(value)
                .font(.system(size: 11.5, design: .monospaced))
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
                .lineLimit(1)
            Spacer(minLength: 8)
            Button(L10n.text("Copy")) {
                copy(value)
            }
            .controlSize(.small)
        }
    }

    private func accessRow(
        title: String,
        detail: String,
        buttonTitle: String,
        action: @escaping () -> Void
    ) -> some View {
        HStack(alignment: .center, spacing: 16) {
            VStack(alignment: .leading, spacing: 3) {
                Text(title)
                    .font(.system(size: 13, weight: .medium))
                Text(detail)
                    .font(.system(size: 11.5))
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 16)
            Button(buttonTitle, action: action)
                .controlSize(.small)
                .disabled(model.isBusy || configuration == nil)
        }
        .padding(.horizontal, 13)
        .padding(.vertical, 12)
    }

    private func copy(_ value: String) {
        let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(trimmed, forType: .string)
    }
}
