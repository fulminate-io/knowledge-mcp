// The contract side of the cross-file conformance case. It sits under
// Sources/<Module>/ deliberately: swift's resolution unit is the build module,
// derived from that layout, and a file written outside it takes FILE scope
// instead — see loose.swift for the fallback case.

public struct Req {}
public struct Resp {}

public protocol Greeter {
    func greet(r: Req) -> Resp
}

public protocol Chatty: Greeter {
}

public class Base {
    public func base() {}
}
