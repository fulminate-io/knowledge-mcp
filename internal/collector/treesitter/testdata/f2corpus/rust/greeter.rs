// The contract side of the cross-file conformance case: the trait is declared
// HERE and implemented in server.rs, so resolving it exercises the import-bind
// rung rather than a same-file lookup.

pub struct Req;
pub struct Resp;

pub trait Greeter {
    fn greet(&self, r: Req) -> Resp;
}

pub fn mk() -> Req {
    Req
}
