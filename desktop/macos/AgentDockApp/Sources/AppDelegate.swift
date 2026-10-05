import AppKit
import ApplicationServices
import Foundation

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private let service = ServiceController()
    private let menuLoginAgent = MenuLoginAgentController()
    private let launchedInBackground = CommandLine.arguments.contains("--background")
    private var statusItem: NSStatusItem?
    private var currentStatus = ServiceStatus.missing
    private var timer: Timer?
    private var isUpdating = false
    private var isCheckingForUpdate = false
    private var trayServiceActionInProgress = false
    private lazy var updateProgressWindow = UpdateProgressWindowController()
    private lazy var setupWindow = NativeControlPanelWindowController(
        service: service,
        menuLoginAgent: menuLoginAgent,
        onChanged: { [weak self] in
            self?.refreshStatus()
        },
        onUpdateRequested: { [weak self] in
            self?.startUpdate()
        }
    )

    func applicationDidFinishLaunching(_ notification: Notification) {
        let recoveryReady = DesktopUpdateTransactionRecovery.recoverIfNeeded(paths: service.paths)
        var pendingUpdateResult = DesktopUpdateResult.load(from: service.paths.updateResult)
        var updateResultExists = FileManager.default.fileExists(atPath: service.paths.updateResult.path)

        // 旧 0.8.x 更新结果没有 transaction id。若用户在更新完成后又手动替换/恢复了 App，
        // 结果文件记录的 target 已不再代表当前磁盘状态；继续按“更新收尾”处理只会永久锁住 UI。
        if recoveryReady,
           let pendingResult = pendingUpdateResult,
           pendingResult.ok,
           pendingResult.transactionID?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ?? true,
           AppVersion.display(pendingResult.targetVersion) != AppVersion.current {
            NSLog(
                "AgentDock found a legacy update result that no longer matches the active App; reconciling the current installation."
            )
            _ = DesktopUpdateResult.consume(from: service.paths.updateResult)
            DesktopUpdateServiceState.remove(at: service.paths.updateServiceState)
            DesktopUpdateHandoff.remove(at: service.paths.updateHandoff)
            pendingUpdateResult = nil
            updateResultExists = false
        }

        if !recoveryReady {
            // Do not acknowledge or clear any pending transaction when crash recovery itself
            // could not establish a safe state. The journal remains intact for repair/retry.
            setUpdateInProgress(true)
            updateProgressWindow.presentFinishing(
                currentVersion: pendingUpdateResult?.currentVersion ?? AppVersion.current,
                targetVersion: pendingUpdateResult?.targetVersion ?? AppVersion.current
            )
            refreshStatus()
        } else if let pendingUpdateResult {
            setUpdateInProgress(true)
            updateProgressWindow.presentFinishing(
                currentVersion: pendingUpdateResult.currentVersion,
                targetVersion: pendingUpdateResult.targetVersion
            )
            restoreBackgroundServicesAfterUpdate(pendingUpdateResult)
        } else if updateResultExists {
            // 结果文件存在但无法解析时，外部更新事务仍可能在等待新版 App ACK。
            // 保留 update-services.json，让外部更新器按超时路径恢复旧 App。
            NSLog("AgentDock 更新结果存在但无法解析，保留后台服务事务状态等待回滚。")
            setUpdateInProgress(true)
            updateProgressWindow.presentFinishing(
                currentVersion: AppVersion.current,
                targetVersion: AppVersion.current
            )
            refreshStatus()
        } else {
            // 没有 pending result 时，更新协调文件只能是上一次已结束流程留下的临时状态。
            // 正常启动到这里才允许显示托盘；更新接管分支始终保持隐藏。
            setUpdateInProgress(false)
            configureMenuLoginAgentIfNeeded()
            DesktopUpdateServiceState.remove(at: service.paths.updateServiceState)
            DesktopUpdateHandoff.remove(at: service.paths.updateHandoff)
            refreshStatus(showWindow: !launchedInBackground)
            if launchedInBackground {
                hideToMenuBar()
            }
            Task {
                do {
                    try service.reconcileTunnelRegistrationFromConfiguration()
                } catch {
                    NSLog("AgentDock 启动时 Tunnel 状态收敛失败：%@", error.localizedDescription)
                }
                await service.ensureCoreProcess()
                self.refreshStatus()
            }
        }
        timer = Timer.scheduledTimer(withTimeInterval: 15, repeats: true) { [weak self] _ in
            Task { @MainActor in
                self?.refreshStatus()
            }
        }
    }

    func applicationWillTerminate(_ notification: Notification) {
        timer?.invalidate()
        // 完全退出才停掉界面拉起的核心。关窗口只收到菜单栏，核心继续在后台跑。
        service.stopAppOwnedCore()
    }

    func hideToMenuBar() {
        // 关窗后只留菜单栏。setActivationPolicy(.accessory) 在当前系统上经常去不掉 Dock 图标，
        // 必须再做一次 UIElement 变换。图标要在变换前就挂上；变换之后新建的会被控制中心当成临时项，
        // 菜单栏排满时临时项不会画出来。安装目录不影响这件事。
        installMenuBarItem()
        var psn = ProcessSerialNumber(highLongOfPSN: 0, lowLongOfPSN: UInt32(kCurrentProcess))
        _ = TransformProcessType(&psn, ProcessApplicationTransformState(kProcessTransformToUIElementApplication))
        _ = NSApp.setActivationPolicy(.accessory)
        DispatchQueue.main.async { [weak self] in
            guard let self else { return }
            if self.statusItem?.button == nil {
                self.installMenuBarItem()
            }
        }
    }

    func showInDock() {
        var psn = ProcessSerialNumber(highLongOfPSN: 0, lowLongOfPSN: UInt32(kCurrentProcess))
        _ = TransformProcessType(&psn, ProcessApplicationTransformState(kProcessTransformToForegroundApplication))
        _ = NSApp.setActivationPolicy(.regular)
        NSApp.unhide(nil)
        DispatchQueue.main.async { [weak self] in
            guard let self, self.statusItem?.button == nil else { return }
            self.installMenuBarItem()
        }
    }

    private func setUpdateInProgress(_ inProgress: Bool, checking: Bool = false) {
        isUpdating = inProgress
        isCheckingForUpdate = inProgress && checking
        setStatusItemVisible(UpdateStatusItemVisibility.shouldShow(
            isUpdating: isUpdating,
            isCheckingForUpdate: isCheckingForUpdate
        ))
        ApplicationMenu.setQuitEnabled(!inProgress)
        setupWindow.setUpdateInProgress(
            inProgress,
            status: checking ? L10n.text("Checking for updates…") : nil
        )
        rebuildMenu()
    }

    private func restoreBackgroundServicesAfterUpdate(_ pendingResult: DesktopUpdateResult) {
        Task {
            var handoffAcknowledged = false
            let transactionID = pendingResult.transactionID?
                .trimmingCharacters(in: .whitespacesAndNewlines)
            do {
                if pendingResult.ok,
                   AppVersion.display(pendingResult.targetVersion) != AppVersion.current {
                    throw ValidationError(L10n.format(
                        "The active AgentDock App version %@ does not match the update target %@.",
                        AppVersion.current,
                        AppVersion.display(pendingResult.targetVersion)
                    ))
                }
                guard let serviceState = try DesktopUpdateServiceState.load(from: service.paths.updateServiceState) else {
                    throw ValidationError(L10n.text("AgentDock update is missing background service recovery state."))
                }

                // Transitional safety for the first release that removes bundled cloudflared:
                // the source updater may predate the component store. During the target trial the
                // old App is still preserved in the rollback slot, so import its signed helper
                // before Tunnel registration is restored or the Arbiter is allowed to commit.
                let configuredMode = (try? service.configuredTunnelMode()) ?? .local
                // Tailcat 和 LAN 不依赖 cloudflared。缺组件不能挡住这两类配置的更新。
                let needsCloudflared = configuredMode == .quick || configuredMode == .named
                try await service.migrateLegacyCloudflaredIfNeeded(
                    source: DesktopUpdateTransactionRecovery.legacyCloudflaredRollbackSource(paths: service.paths),
                    required: needsCloudflared || serviceState.tunnelEnabled
                )

                // Restore Bundle-owned SMAppService definitions first. requiresApproval is an
                // explicit policy state and is reported to the Arbiter instead of failing the App.
                let registration = try service.restoreBackgroundServiceRegistrationsForUpdate(
                    coreEnabled: serviceState.coreEnabled,
                    tunnelEnabled: serviceState.tunnelEnabled
                )
                // SMAppService.status == enabled does not prove launchd has actually started Core.
                // Perform one bounded health/self-heal pass before publishing the handoff so the
                // Arbiter only starts its final health/version gate after registration has settled.
                var warnings = await service.recoverBackgroundServicesAfterUpdate(
                    coreEnabled: serviceState.coreEnabled,
                    tunnelEnabled: serviceState.tunnelEnabled
                )
                if pendingResult.ok {
                    try DesktopUpdateHandoff(
                        targetVersion: pendingResult.targetVersion,
                        transactionID: pendingResult.transactionID,
                        coreRegistration: registration.core,
                        tunnelRegistration: registration.tunnel
                    ).write(to: service.paths.updateHandoff)
                    handoffAcknowledged = true
                } else if let transactionID = pendingResult.transactionID,
                          !transactionID.isEmpty {
                    // Rollback 也必须由恢复后的 source App 明确 ACK。这样 Arbiter 只有在
                    // SMAppService 已重新绑定回 source Bundle 后，才允许持久化 rolled_back。
                    try DesktopUpdateHandoff(
                        targetVersion: pendingResult.currentVersion,
                        transactionID: transactionID,
                        coreRegistration: registration.core,
                        tunnelRegistration: registration.tunnel
                    ).write(to: service.paths.updateHandoff)
                    handoffAcknowledged = true
                }

                if registration.core == "requires_approval" {
                    warnings.append(L10n.text("AgentDock Core needs background-item approval in System Settings."))
                }

                // Tunnel/public access is a soft dependency. Reconcile it best-effort, but surface
                // readiness only in logs and the control panel; it must not gate or decorate an
                // otherwise successful install/update result.
                do {
                    try service.reconcileTunnelRegistrationFromConfiguration()
                } catch {
                    NSLog("AgentDock 更新后 Tunnel 状态收敛失败：%@", error.localizedDescription)
                }

                if let transactionID, !transactionID.isEmpty {
                    guard let terminalResult = await waitForUpdateTerminalResult(transactionID: transactionID) else {
                        // handoff 只证明当前 Bundle 能启动并恢复注册，不代表事务已经提交。
                        // 没有统一 terminal result 时保留 journal/rollback slot，禁止提前显示成功。
                        updateProgressWindow.showFailure(L10n.text(
                            "AgentDock update transaction did not report a final result. Recovery state was preserved."
                        ))
                        refreshStatus()
                        return
                    }
                    guard AppVersion.display(terminalResult.sourceVersion) == AppVersion.display(pendingResult.currentVersion),
                          AppVersion.display(terminalResult.targetVersion) == AppVersion.display(pendingResult.targetVersion) else {
                        updateProgressWindow.showFailure(L10n.text(
                            "AgentDock update transaction final result did not match the pending update. Recovery state was preserved."
                        ))
                        refreshStatus()
                        return
                    }
                    for warning in terminalResult.warnings ?? [] {
                        let value = warning.trimmingCharacters(in: .whitespacesAndNewlines)
                        if !value.isEmpty, !warnings.contains(value) {
                            warnings.append(value)
                        }
                    }

                    switch terminalResult.state {
                    case "committed":
                        guard pendingResult.ok,
                              AppVersion.display(terminalResult.targetVersion) == AppVersion.current else {
                            presentTerminalUpdateFailure(
                                pendingResult: pendingResult,
                                terminalResult: terminalResult,
                                warnings: warnings,
                                keepUpdateLocked: true
                            )
                            return
                        }
                        _ = DesktopUpdateResult.consume(from: service.paths.updateResult)
                        DesktopUpdateServiceState.remove(at: service.paths.updateServiceState)
                        if let menuLoginWarning = await restoreMenuLoginAgentAfterUpdateCommit() {
                            warnings.append(menuLoginWarning)
                        }
                        self.presentUpdateResult(
                            pendingResult,
                            warning: warnings.isEmpty ? nil : warnings.joined(separator: "\n")
                        )
                    case "rolled_back":
                        _ = DesktopUpdateResult.consume(from: service.paths.updateResult)
                        DesktopUpdateServiceState.remove(at: service.paths.updateServiceState)
                        presentTerminalUpdateFailure(
                            pendingResult: pendingResult,
                            terminalResult: terminalResult,
                            warnings: warnings,
                            keepUpdateLocked: false
                        )
                    case "failed":
                        // rollback 自身失败时保留 pending result/service-state；repair 仍需要这些证据。
                        presentTerminalUpdateFailure(
                            pendingResult: pendingResult,
                            terminalResult: terminalResult,
                            warnings: warnings,
                            keepUpdateLocked: true
                        )
                    default:
                        return
                    }
                    return
                }

                // 一次性兼容 pre-transaction 0.8.x。新架构只认 update/result.json 的最终状态。
                guard let result = DesktopUpdateResult.consume(from: service.paths.updateResult) else {
                    throw ValidationError(L10n.text("AgentDock update result was lost while restoring background services."))
                }
                DesktopUpdateServiceState.remove(at: service.paths.updateServiceState)
                if pendingResult.ok, let menuLoginWarning = await restoreMenuLoginAgentAfterUpdateCommit() {
                    warnings.append(menuLoginWarning)
                }
                self.presentUpdateResult(result, warning: warnings.isEmpty ? nil : warnings.joined(separator: "\n"))
            } catch {
                NSLog("AgentDock 更新后后台服务恢复失败：%@", error.localizedDescription)
                if let transactionID, !transactionID.isEmpty {
                    // transaction-aware 流程在 ACK/terminal 之前失败时必须保留 Updating 与 journal。
                    // 外部 Arbiter 会继续 rollback；若 rollback 本身失败，repair 仍有完整恢复证据。
                    updateProgressWindow.showFailure(error.localizedDescription)
                    refreshStatus()
                    return
                }
                if pendingResult.ok, handoffAcknowledged {
                    // 仅供 pre-transaction 0.8.x 兼容。新架构绝不把 handoff 当成最终成功。
                    self.presentUpdateResult(
                        pendingResult,
                        warning: L10n.format(
                            "Background services have not been restored yet. AgentDock will try again at the next launch: %@",
                            error.localizedDescription
                        )
                    )
                    return
                }
                if !pendingResult.ok {
                    self.presentUpdateResult(
                        pendingResult,
                        warning: L10n.format(
                            "The update process returned, but restoring background services failed: %@",
                            error.localizedDescription
                        )
                    )
                    return
                }
                self.refreshStatus()
            }
        }
    }

    private func waitForUpdateTerminalResult(
        transactionID: String,
        timeout: TimeInterval = 210
    ) async -> DesktopUpdateTerminalResult? {
        let deadline = Date().addingTimeInterval(timeout)
        var nextRecoveryProbe = Date().addingTimeInterval(5)
        while Date() < deadline {
            if let result = DesktopUpdateTerminalResult.load(
                from: service.paths.updateTerminalResult,
                transactionID: transactionID
            ) {
                return result
            }
            // transaction.json is the durable commit point. result.json is a projection for
            // desktop clients and may be missing if the Arbiter exits between the two atomic
            // writes. The transaction carries the same terminal fields, so consume it directly
            // instead of turning a completed update into a four-minute UI timeout.
            if let result = DesktopUpdateTerminalResult.load(
                from: service.paths.updateTransaction,
                transactionID: transactionID
            ) {
                return result
            }

            if Date() >= nextRecoveryProbe {
                let paths = service.paths
                await withCheckedContinuation { (continuation: CheckedContinuation<Void, Never>) in
                    DispatchQueue.global(qos: .utility).async {
                        // 正常更新时 source Arbiter 持有 transaction.lock，此探针立即无害返回；
                        // 若 Arbiter 崩溃，则由同一 known-good source Arbiter 保守接管 rollback。
                        _ = DesktopUpdateTransactionRecovery.recoverIfNeeded(paths: paths)
                        continuation.resume()
                    }
                }
                nextRecoveryProbe = Date().addingTimeInterval(5)
            }
            try? await Task.sleep(nanoseconds: 250_000_000)
        }
        return nil
    }

    private func presentTerminalUpdateFailure(
        pendingResult: DesktopUpdateResult,
        terminalResult: DesktopUpdateTerminalResult,
        warnings: [String],
        keepUpdateLocked: Bool
    ) {
        if !keepUpdateLocked {
            setUpdateInProgress(false)
        }
        var messages: [String] = []
        if !pendingResult.ok, !pendingResult.message.isEmpty {
            messages.append(pendingResult.message)
        }
        if let failure = terminalResult.failure?.message.trimmingCharacters(in: .whitespacesAndNewlines),
           !failure.isEmpty {
            messages.append(failure)
        }
        messages.append(contentsOf: warnings.filter { !$0.isEmpty })
        if messages.isEmpty {
            messages.append(L10n.text("Update failed"))
        }
        updateProgressWindow.showFailure(messages.joined(separator: "\n\n"))
        refreshStatus()
    }

    private func restoreMenuLoginAgentAfterUpdateCommit() async -> String? {
        if ProcessInfo.processInfo.environment["AGENTDOCK_SKIP_LOGIN_ITEM_CONFIGURATION"] == "1" {
            return nil
        }
        let fileManager = FileManager.default
        let deadline = Date().addingTimeInterval(10)
        while fileManager.fileExists(atPath: service.paths.updateHandoff.path), Date() < deadline {
            try? await Task.sleep(nanoseconds: 100_000_000)
        }
        guard !fileManager.fileExists(atPath: service.paths.updateHandoff.path) else {
            let message = L10n.text("Menu bar launch at sign-in will be reconciled at the next launch because the update transaction is still in progress.")
            NSLog("AgentDock 更新后菜单栏登录启动延后恢复：更新事务尚未完成。")
            return message
        }

        do {
            try menuLoginAgent.restoreAfterUpdate()
            return nil
        } catch {
            NSLog("AgentDock 更新后菜单栏登录启动恢复失败：%@", error.localizedDescription)
            return L10n.format("Menu bar launch at sign-in could not be restored: %@", error.localizedDescription)
        }
    }

    private func configureMenuLoginAgentIfNeeded() {
        if ProcessInfo.processInfo.environment["AGENTDOCK_SKIP_LOGIN_ITEM_CONFIGURATION"] == "1" {
            return
        }
        do {
            try menuLoginAgent.configureOnLaunch()
        } catch {
            // 菜单栏登录启动失败不影响 Core 后台服务；用户仍可手动打开 AgentDock。
            NSLog("AgentDock 菜单栏登录启动配置失败：%@", error.localizedDescription)
        }
    }

    private func installMenuBarItem() {
        guard UpdateStatusItemVisibility.shouldShow(
            isUpdating: isUpdating,
            isCheckingForUpdate: isCheckingForUpdate
        ) else {
            removeStatusItem()
            return
        }
        ensureStatusItem()
        rebuildMenu()
    }

    private func prepareMenuBarItemPosition() {
        let defaults = UserDefaults.standard
        let visibleKey = "NSStatusItem Visible AgentDockTray"
        let positionKey = "NSStatusItem Preferred Position AgentDockTray"
        if defaults.object(forKey: visibleKey) == nil {
            defaults.set(true, forKey: visibleKey)
        }
        // 控制中心给新图标的默认位置落在刘海正下方，创建成功也看不见。
        // 第一次放到屏幕右侧、时钟左边。之后以用户拖动或「菜单栏」设置里保存的位置为准。
        guard defaults.object(forKey: positionKey) == nil else { return }
        let width = NSScreen.screens.map(\.frame.maxX).max() ?? 1440
        defaults.set(width - 380, forKey: positionKey)
    }

    private func ensureStatusItem() {
        if statusItem == nil {
            // 固定名字后控制中心才会记住位置。没有名字的图标是临时项，菜单栏排满时会被挤进刘海。
            // 若用户在「菜单栏」设置里关掉 AgentDock，仍以系统设置为准。
            prepareMenuBarItemPosition()
            let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
            item.autosaveName = "AgentDockTray"
            item.isVisible = true
            statusItem = item
        }
        guard let button = statusItem?.button else { return }
        button.image = AgentDockLogoArtwork.menuBarImage()
        button.image?.isTemplate = true
        button.imageScaling = .scaleProportionallyDown
        button.imagePosition = .imageOnly
        button.toolTip = "AgentDock"
        statusItem?.isVisible = true
    }

    private func removeStatusItem() {
        guard let item = statusItem else { return }
        NSStatusBar.system.removeStatusItem(item)
        statusItem = nil
    }

    private func setStatusItemVisible(_ visible: Bool) {
        if visible {
            installMenuBarItem()
            return
        }
        removeStatusItem()
    }

    private func refreshStatus(showWindow: Bool = false) {
        Task {
            let status = await service.status()
            await MainActor.run {
                self.currentStatus = status
                self.rebuildMenu()
                if showWindow {
                    self.setupWindow.present(status: status)
                } else if self.setupWindow.window?.isVisible == true {
                    self.setupWindow.refreshServiceStatus(status)
                }
            }
        }
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        // 关窗不退出。窗口自己会收到菜单栏，后台服务继续运行。
        false
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        if isUpdating {
            if isCheckingForUpdate {
                setupWindow.present(status: currentStatus)
            } else {
                updateProgressWindow.present()
            }
            return true
        }
        setupWindow.present(status: currentStatus)
        return true
    }

    private func rebuildMenu() {
        let menu = NSMenu()
        if isUpdating {
            let activity = isCheckingForUpdate ? L10n.text("Checking for updates…") : L10n.text("Updating…")
            let statusMenuItem = NSMenuItem(
                title: L10n.format("AgentDock: %@", activity),
                action: nil,
                keyEquivalent: ""
            )
            statusMenuItem.isEnabled = false
            menu.addItem(statusMenuItem)
            menu.addItem(.separator())
            if !isCheckingForUpdate {
                menu.addItem(item(L10n.text("Show update progress"), #selector(showUpdateProgress)))
            }
            if currentStatus.installed {
                menu.addItem(item(L10n.text("Open logs folder"), #selector(openLogs)))
            }
            statusItem?.menu = menu
            return
        }

        let statusText: String
        if !currentStatus.installed {
            statusText = L10n.text("Not installed")
        } else if currentStatus.healthy {
            if AppVersion.matchesHealthVersion(currentStatus.version) {
                statusText = L10n.text("Running normally")
            } else {
                statusText = L10n.format(
                    "Version mismatch · AgentDock %@ · Core %@",
                    AppVersion.current,
                    AppVersion.display(currentStatus.version)
                )
            }
        } else if currentStatus.requiresApproval {
            statusText = L10n.text("Background permission required")
        } else if currentStatus.loaded {
            statusText = L10n.text("Service error")
        } else {
            statusText = L10n.text("Stopped")
        }
        let statusMenuItem = NSMenuItem(title: L10n.format("AgentDock: %@", statusText), action: nil, keyEquivalent: "")
        statusMenuItem.isEnabled = false
        menu.addItem(statusMenuItem)
        menu.addItem(.separator())

        menu.addItem(item(currentStatus.installed ? L10n.text("Open AgentDock") : L10n.text("Set up AgentDock…"), #selector(showSetup)))
        menu.addItem(item(L10n.text("Check permissions"), #selector(openPermissions)))
        if currentStatus.installed {
            menu.addItem(.separator())

            if currentStatus.requiresApproval {
                menu.addItem(item(L10n.text("Open background settings"), #selector(openBackgroundSettings)))
            } else if currentStatus.loaded {
                menu.addItem(item(L10n.text("Stop AgentDock"), #selector(stopService)))
                menu.addItem(item(L10n.text("Restart AgentDock"), #selector(restartService)))
            } else {
                menu.addItem(item(L10n.text("Start AgentDock"), #selector(startService)))
            }
            menu.addItem(item(L10n.text("Check for updates…"), #selector(updateService)))
            menu.addItem(.separator())
            if currentStatus.loaded {
                menu.addItem(item(L10n.text("View activity"), #selector(showActivity)))
            }
            menu.addItem(item(L10n.text("Open logs folder"), #selector(openLogs)))
            menu.addItem(item(L10n.text("Open configuration folder"), #selector(openConfiguration)))
        }
        menu.addItem(item(L10n.text("Open documentation"), #selector(openDocumentation)))
        menu.addItem(.separator())
        menu.addItem(item(L10n.text("Quit completely"), #selector(quit)))
        statusItem?.menu = menu
    }

    private func item(_ title: String, _ action: Selector) -> NSMenuItem {
        let menuItem = NSMenuItem(title: title, action: action, keyEquivalent: "")
        menuItem.target = self
        return menuItem
    }

    @objc private func showSetup() { setupWindow.present(status: currentStatus) }
    @objc private func showUpdateProgress() { updateProgressWindow.present() }
    @objc private func openPermissions() { setupWindow.presentPermissions() }
    @objc private func showActivity() { setupWindow.presentActivity(status: currentStatus) }
    @objc private func openLogs() { service.openLogs() }
    @objc private func openConfiguration() { service.openConfiguration() }
    @objc private func openBackgroundSettings() { service.openBackgroundItemsSettings() }

    @objc private func openDocumentation() {
        if let url = URL(string: "https://docs.nexusdock.co/agentdock/") {
            NSWorkspace.shared.open(url)
        }
    }

    @objc private func startService() { performServiceAction(L10n.text("Start")) { try await self.service.start() } }
    @objc private func stopService() { performServiceAction(L10n.text("Stop")) { try await self.service.stop() } }
    @objc private func restartService() { performServiceAction(L10n.text("Restart")) { try await self.service.restart() } }

    @objc private func updateService() {
        startUpdate()
    }

    private func startUpdate() {
        guard !isUpdating else {
            if isCheckingForUpdate {
                setupWindow.present(status: currentStatus)
            } else {
                updateProgressWindow.present()
            }
            return
        }
        guard !trayServiceActionInProgress, !setupWindow.hasActiveServiceOperation else {
            presentAlert(
                title: L10n.text("AgentDock is busy"),
                message: L10n.text("Wait for the current AgentDock operation to finish before starting an update.")
            )
            return
        }

        // “检查更新”只做版本检查。下载、停服务和 App 替换必须等用户明确确认。
        setUpdateInProgress(true, checking: true)
        Task {
            do {
                let check = try await service.checkForUpdates()
                let shouldApply = await MainActor.run {
                    guard check.updateAvailable else {
                        self.setUpdateInProgress(false)
                        self.presentAlert(
                            title: L10n.text("AgentDock is up to date"),
                            message: check.message
                        )
                        self.refreshStatus()
                        return false
                    }
                    guard self.confirmUpdate(check) else {
                        self.setUpdateInProgress(false)
                        self.refreshStatus()
                        return false
                    }
                    self.setUpdateInProgress(true)
                    self.updateProgressWindow.presentChecking()
                    return true
                }
                guard shouldApply else { return }

                _ = try await service.applyUpdate { [weak self] event in
                    Task { @MainActor in
                        self?.updateProgressWindow.apply(event)
                    }
                }
                await MainActor.run {
                    self.setUpdateInProgress(false)
                    self.refreshStatus()
                }
            } catch {
                await MainActor.run {
                    let failedWhileChecking = self.isCheckingForUpdate
                    self.setUpdateInProgress(false)
                    if failedWhileChecking {
                        self.presentAlert(
                            title: L10n.text("Check for updates"),
                            message: error.localizedDescription,
                            style: .warning
                        )
                    } else {
                        self.updateProgressWindow.showFailure(error.localizedDescription)
                    }
                    self.refreshStatus()
                }
            }
        }
    }

    private func confirmUpdate(_ check: DesktopUpdateCheck) -> Bool {
        let currentVersion = check.currentVersion ?? L10n.text("Unknown version")
        let latestVersion = check.latestVersion ?? L10n.text("Unknown version")
        let alert = NSAlert()
        alert.messageText = L10n.text("AgentDock Update")
        alert.informativeText = L10n.format(
            "A new AgentDock version is available.\n\nCurrent version: %@\nLatest version: %@\n\nUpdate now?",
            currentVersion,
            latestVersion
        )
        alert.alertStyle = .informational
        alert.addButton(withTitle: L10n.text("Update"))
        alert.addButton(withTitle: L10n.text("Cancel"))
        return alert.runModal() == .alertFirstButtonReturn
    }

    private func performServiceAction(_ action: String, operation: @escaping () async throws -> Void) {
        guard !isUpdating else {
            updateProgressWindow.present()
            return
        }
        guard !trayServiceActionInProgress else { return }
        trayServiceActionInProgress = true
        Task {
            do {
                try await operation()
                try? await Task.sleep(nanoseconds: 800_000_000)
                await MainActor.run {
                    self.trayServiceActionInProgress = false
                    self.refreshStatus()
                }
            } catch {
                await MainActor.run {
                    self.trayServiceActionInProgress = false
                    self.presentAlert(
                        title: L10n.format("%@ failed", action),
                        message: error.localizedDescription,
                        style: .warning
                    )
                }
            }
        }
    }

    private func presentUpdateResult(_ result: DesktopUpdateResult, warning: String? = nil) {
        setUpdateInProgress(false)
        if result.ok {
            updateProgressWindow.showCompletion(targetVersion: result.targetVersion, warning: warning)
        } else {
            let message = [result.message, warning]
                .compactMap { $0 }
                .joined(separator: "\n\n")
            updateProgressWindow.showFailure(message)
        }
        refreshStatus()
    }

    private func presentAlert(title: String, message: String, style: NSAlert.Style = .informational) {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = message
        alert.alertStyle = style
        alert.runModal()
    }

    @objc private func quit() {
        guard !isUpdating else {
            updateProgressWindow.present()
            return
        }
        NSApp.terminate(nil)
    }
}
