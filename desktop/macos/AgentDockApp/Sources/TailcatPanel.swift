import Foundation

struct TailcatPanelState {
    var port: Int
    var allowText: String
    var address: String
    var error: String
    var running: Bool

    static let empty = TailcatPanelState(port: 80, allowText: "", address: "", error: "", running: false)
}

enum TailcatPanel {
    static func load(paths: AppPaths) -> TailcatPanelState {
        var state = TailcatPanelState.empty
        if let config = jsonObject(at: paths.tailcatConfig) {
            if let port = config["port"] as? Int, (1...65535).contains(port) {
                state.port = port
            } else if let portNumber = config["port"] as? NSNumber {
                let port = portNumber.intValue
                if (1...65535).contains(port) {
                    state.port = port
                }
            }
            state.allowText = allowText(config["allow"])
        }
        if let status = jsonObject(at: paths.tailcatStatus) {
            if let port = status["port"] as? Int, (1...65535).contains(port) {
                state.port = port
            } else if let portNumber = status["port"] as? NSNumber {
                let port = portNumber.intValue
                if (1...65535).contains(port) {
                    state.port = port
                }
            }
            if let allow = allowText(status["allow"]) as String?, !allow.isEmpty {
                state.allowText = allow
            }
            state.address = (status["address"] as? String)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            state.error = (status["error"] as? String)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            state.running = (status["running"] as? Bool) ?? false
        }
        return state
    }

    static func validate(portText: String, allowText: String) throws -> (port: Int, allow: [String]) {
        let trimmedPort = portText.trimmingCharacters(in: .whitespacesAndNewlines)
        let port = Int(trimmedPort) ?? 0
        guard (1...65535).contains(port) else {
            throw ValidationError(L10n.text("The Tailcat port must be between 1 and 65535."))
        }
        let allow = try parseAllow(allowText)
        return (port, allow)
    }

    static func configData(port: Int, allow: [String]) throws -> Data {
        let payload: [String: Any] = ["port": port, "allow": allow]
        let data = try JSONSerialization.data(withJSONObject: payload, options: [.prettyPrinted, .sortedKeys])
        return data + Data("\n".utf8)
    }

    private static func parseAllow(_ text: String) throws -> [String] {
        let separators = CharacterSet.whitespacesAndNewlines.union(CharacterSet(charactersIn: ","))
        let fields = text.components(separatedBy: separators).map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty }
        guard fields.count <= 128 else {
            throw ValidationError(L10n.text("The Tailcat allow list accepts at most 128 clients."))
        }
        var seen = Set<String>()
        var allow: [String] = []
        for field in fields {
            guard field.range(of: #"^nodekey:[0-9a-fA-F]{64}$"#, options: .regularExpression) != nil else {
                throw ValidationError(L10n.text("A Tailcat allowed client must be nodekey: followed by 64 hexadecimal digits."))
            }
            let canonical = "nodekey:" + field.dropFirst("nodekey:".count).lowercased()
            guard seen.insert(canonical).inserted else {
                throw ValidationError(L10n.text("The Tailcat allow list contains a duplicate client."))
            }
            allow.append(canonical)
        }
        return allow.sorted()
    }

    private static func allowText(_ value: Any?) -> String {
        if let values = value as? [String] {
            return values.joined(separator: "\n")
        }
        if let values = value as? [Any] {
            return values.compactMap { $0 as? String }.joined(separator: "\n")
        }
        return ""
    }

    private static func jsonObject(at url: URL) -> [String: Any]? {
        guard let data = try? Data(contentsOf: url),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return nil
        }
        return object
    }
}
