#include <cstdio>

#include "greeter.hpp"
#include "mathutil.h"

int main() {
  Greeter g("World");
  int result = add(2, 3);
  std::printf("%s %d\n", g.greet().c_str(), result);
  return 0;
}
