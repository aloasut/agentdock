import Foundation

struct ManagedEnvironment {
    let originalText: String
    let values: [String: String]

    static func load(from url: URL) throws -> ManagedEnvironment {
        let data = try Data(contentsOf: url)
        guard let text = String(data: data, encoding: .utf8) else {
            throw ValidationError(L10n.text("AgentDock configuration is not valid UTF-8 text."))
        }
        return ManagedEnvironment(originalText: text, values: parseValues(text))
    }

    func dataByUpdating(
        _ replacements: [String: String],
        removing removals: Set<String> = [],
        allowedKeys extraAllowedKeys: Set<String> = []
    ) throws -> Data {
        // AGENTDOCK_HOST 不进通用可编辑名单。局域网按钮只能经 LANListenConfiguration
        // 写成 lan 或 127.0.0.1，避免设置页顺手改掉监听地址。
        let editable = Set(ServiceConfiguration.editableKeys).union(extraAllowedKeys)
        let requested = Set(replacements.keys)
        guard requested.isSubset(of: editable) else {
            let rejected = requested.subtracting(editable).sorted().joined(separator: ", ")
            throw ValidationError(L10n.format("Contains configuration keys that the GUI is not allowed to modify: %@", rejected))
        }
        let removable = editable.union(ServiceConfiguration.removableLegacyKeys)
        guard removals.isSubset(of: removable) else {
            let rejected = removals.subtracting(removable).sorted().joined(separator: ", ")
            throw ValidationError(L10n.format("Contains configuration keys that the GUI is not allowed to remove: %@", rejected))
        }
        guard requested.isDisjoint(with: removals) else {
            throw ValidationError(L10n.text("The same configuration key cannot be updated and removed at the same time."))
        }

        var pending = replacements
        var output: [String] = []
        let lines = originalText.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        for line in lines {
            guard let key = Self.assignmentKey(line) else {
                output.append(line)
                continue
            }
            if removals.contains(key) {
                continue
            }
            guard replacements.keys.contains(key) else {
                output.append(line)
                continue
            }
            guard let replacement = pending.removeValue(forKey: key) else {
                // 已处理过该键，清理后续重复定义。
                continue
            }
            output.append("\(key)=\(Self.shellQuote(replacement))")
        }
        for key in pending.keys.sorted() {
            if let value = pending[key] {
                output.append("\(key)=\(Self.shellQuote(value))")
            }
        }
        while output.last == "" { output.removeLast() }
        output.append("")
        guard let data = output.joined(separator: "\n").data(using: .utf8) else {
            throw ValidationError(L10n.text("Unable to encode the AgentDock configuration file."))
        }
        return data
    }

    static func parseValues(_ text: String) -> [String: String] {
        var values: [String: String] = [:]
        for rawLine in text.split(whereSeparator: \.isNewline) {
            var line = rawLine.trimmingCharacters(in: .whitespaces)
            if line.isEmpty || line.hasPrefix("#") { continue }
            if line.hasPrefix("export ") {
                line = String(line.dropFirst("export ".count)).trimmingCharacters(in: .whitespaces)
            }
            guard let separator = line.firstIndex(of: "=") else { continue }
            let key = String(line[..<separator]).trimmingCharacters(in: .whitespaces)
            guard key.range(of: "^[A-Z_][A-Z0-9_]*$", options: .regularExpression) != nil else { continue }
            let rawValue = String(line[line.index(after: separator)...])
            values[key] = decodeShellValue(rawValue)
        }
        return values
    }

    private static func assignmentKey(_ rawLine: String) -> String? {
        var line = rawLine.trimmingCharacters(in: .whitespaces)
        if line.isEmpty || line.hasPrefix("#") { return nil }
        if line.hasPrefix("export ") {
            line = String(line.dropFirst("export ".count)).trimmingCharacters(in: .whitespaces)
        }
        guard let separator = line.firstIndex(of: "=") else { return nil }
        let key = String(line[..<separator]).trimmingCharacters(in: .whitespaces)
        guard key.range(of: "^[A-Z_][A-Z0-9_]*$", options: .regularExpression) != nil else { return nil }
        return key
    }

    static func shellQuote(_ value: String) -> String {
        if value.isEmpty { return "''" }
        return "'" + value.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }

    static func decodeShellValue(_ raw: String) -> String {
        let input = Array(raw.trimmingCharacters(in: .whitespaces))
        var result = ""
        var index = 0
        var quote: Character?
        while index < input.count {
            let character = input[index]
            if let activeQuote = quote {
                if character == activeQuote {
                    quote = nil
                } else if activeQuote == "\"", character == "\\", index + 1 < input.count {
                    index += 1
                    result.append(input[index])
                } else {
                    result.append(character)
                }
            } else if character == "'" || character == "\"" {
                quote = character
            } else if character == "\\", index + 1 < input.count {
                index += 1
                result.append(input[index])
            } else {
                result.append(character)
            }
            index += 1
        }
        return result
    }
}

enum LANListenConfiguration {
    /// 局域网 MCP 和 Tailcat 是两条通道。这里只改监听地址，不读也不写隧道模式。
    static func updatedData(_ environment: ManagedEnvironment, enabled: Bool) throws -> Data {
        let target = enabled ? "lan" : "127.0.0.1"
        return try environment.dataByUpdating(
            ["AGENTDOCK_HOST": target],
            allowedKeys: ["AGENTDOCK_HOST"]
        )
    }
}
