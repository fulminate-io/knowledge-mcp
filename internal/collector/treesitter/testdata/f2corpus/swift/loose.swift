// OUTSIDE ANY Sources SEGMENT, deliberately. A swift file that does not fit the
// package layout convention falls back to FILE scope, which is the narrower and
// therefore safe answer. Its conformance is same-file so it still resolves, and
// having it here is what keeps the fallback path exercised rather than assumed.

public protocol Local {
    func handle(v: Value) -> Out
}

public struct Value {}
public struct Out {}

public class Loose: Local {
    public func handle(v: Value) -> Out {
        return v.convert()
    }
}

public func run(l: Local, v: Value) {
    l.handle(v: v)
}
