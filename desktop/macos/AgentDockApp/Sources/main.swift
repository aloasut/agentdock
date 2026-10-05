import AppKit
import Darwin
import Foundation

if CommandLine.arguments.contains("--unregister-background-services") {
    var failures: [String] = []
    MainActor.assumeIsolated {
        do {
            try ServiceController().unregisterManagedBackgroundServicesForUninstall()
        } catch {
            failures.append(error.localizedDescription)
        }
        do {
            try MenuLoginAgentController().unregisterForUninstall()
        } catch {
            failures.append(error.localizedDescription)
        }
    }
    if failures.isEmpty {
        exit(0)
    }
    let message = failures.joined(separator: "\n") + "\n"
    FileHandle.standardError.write(Data(message.utf8))
    exit(1)
}

MainActor.assumeIsolated {
    let application = NSApplication.shared
    AppAppearance.applyStoredPreference()
    ApplicationMenu.install()
    let delegate = AppDelegate()
    application.delegate = delegate
    // 打开窗口时留在 Dock。关掉窗口后会改成菜单栏驻留，避免一开始就没有图标。
    application.setActivationPolicy(.regular)
    application.run()
}
