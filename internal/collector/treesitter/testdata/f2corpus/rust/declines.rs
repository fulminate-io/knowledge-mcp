// The shapes the rust arm must DECLINE. Without these the corpus would be a
// set of inputs designed to agree with the implementation.

use crate::greeter::Req;

pub struct Bag;

impl Bag {
    // An unannotated binding names no declared type, and a container type is
    // not a declaration a method can be looked up on. Neither may bind.
    pub fn run(&self, r: Req) {
        let inferred = make();
        let items: Vec<Req> = Vec::new();
        let pair: (Req, Req) = split();
        let (left, right): (Req, Req) = split();
        inferred.go();
        items.len();
        pair.0;
        left.go();
        right.go();
        r.go();
    }
}
