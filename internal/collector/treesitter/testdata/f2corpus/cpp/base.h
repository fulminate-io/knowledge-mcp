// The contract side of the cross-HEADER conformance case: the abstract bases
// are declared here and the derived class lives in server.cpp, which is the
// defining C++ layout and the population a same-file answer misses entirely.

class Req {};
class Resp {};

class Greeter {
 public:
  virtual Resp greet(Req r) = 0;
};

class Mixin {
 public:
  virtual void mix() = 0;
};

// THE DECLINE CASE: a concrete base declares no pure virtual, so it is not a
// contract and a class deriving from it emits nothing.
class Concrete {
 public:
  void plain();
};
