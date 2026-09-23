import Darwin
import Foundation

/// 枚举本机当前私网 IPv4 地址，供 LAN 模式的连接信息展示。
/// 与 Core 的 lan 监听语义保持一致：仅 RFC1918 网段、UP 且非回环的接口。
enum LANAddresses {
    static func privateIPv4Hosts() -> [String] {
        var hosts: [String] = []
        var seen: Set<String> = []
        var interfaceList: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&interfaceList) == 0, let first = interfaceList else { return hosts }
        defer { freeifaddrs(first) }

        var current: UnsafeMutablePointer<ifaddrs>? = first
        while let entry = current {
            defer { current = entry.pointee.ifa_next }
            let flags = entry.pointee.ifa_flags
            guard flags & UInt32(IFF_UP) != 0, flags & UInt32(IFF_LOOPBACK) == 0,
                  let address = entry.pointee.ifa_addr else { continue }
            guard address.pointee.sa_family == sa_family_t(AF_INET) else { continue }

            var socketAddress = sockaddr_in()
            memcpy(&socketAddress, address, MemoryLayout<sockaddr_in>.size)
            var buffer = [CChar](repeating: 0, count: Int(INET_ADDRSTRLEN))
            guard inet_ntop(AF_INET, &socketAddress.sin_addr, &buffer, socklen_t(INET_ADDRSTRLEN)) != nil else { continue }
            let host = String(cString: buffer)
            guard isPrivateIPv4(host), !seen.contains(host) else { continue }
            seen.insert(host)
            hosts.append(host)
        }
        return hosts
    }

    private static func isPrivateIPv4(_ host: String) -> Bool {
        let parts = host.split(separator: ".").compactMap { Int($0) }
        guard parts.count == 4 else { return false }
        if parts[0] == 10 { return true }
        if parts[0] == 172, (12...27).contains(parts[1]) { return true }
        if parts[0] == 192, parts[1] == 168 { return true }
        return false
    }
}
