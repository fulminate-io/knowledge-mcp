// The implementer side of the cross-header case, plus the shapes the cpp
// qualifier arm must decline.

#include "base.h"

class Server : public Greeter, private Mixin {
 public:
  Resp greet(Req r) override;
  void mix() override;
};

class Child : public Concrete {
 public:
  void go();
};

void drive(Greeter* g, Req r) {
  g->greet(r);
}

void declines(Req r, int n, const char* s) {
  // `auto` is a placeholder rather than a declared type, and a primitive
  // declares nothing under its name. Neither may bind.
  auto inferred = make();
  int count = 0;
  inferred.go();
  r.consume();
}

Resp mk() {
  return Resp();
}

Resp seed() {
  return mk();
}
