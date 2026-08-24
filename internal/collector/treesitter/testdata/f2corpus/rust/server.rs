// The implementer side of the cross-file conformance case.

use crate::greeter::Greeter;
use crate::greeter::Req;
use crate::greeter::Resp;

pub struct Server;

impl Greeter for Server {
    fn greet(&self, r: Req) -> Resp {
        r.consume()
    }
}

impl Server {
    fn helper(&self, r: Req) {
        self.greet(r);
    }
}

pub fn drive(g: &dyn Greeter, r: Req) {
    g.greet(r);
}
