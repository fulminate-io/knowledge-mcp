// The implementer side. Two files of one module see each other with no import
// at all, which is why swift needs a module scope rather than a bind.
//
// `Base` is the DECLINE case: it is a concrete class, so the base-class half of
// the inheritance clause resolves to a non-contract and emits nothing.

public class Server: Base, Greeter {
    public func greet(r: Req) -> Resp {
        return mk()
    }
}

extension Server: Chatty {
    public func extra(r: Req) {
        r.consume()
    }
}

public func drive(g: Greeter, r: Req) {
    g.greet(r: r)
}

public func mk() -> Resp {
    return Resp()
}

public func seed() -> Resp {
    return mk()
}
