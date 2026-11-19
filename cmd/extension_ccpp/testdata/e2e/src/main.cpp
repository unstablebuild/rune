#include <cstdio>

#include "shape.hpp"
#include "util.h"

enum class Color { Red, Green, Blue };

static const char* color_name(Color c) {
  switch (c) {
    case Color::Red:
      return "red";
  }
  return "other";
}

int main() {
  geo::Shape shape(3.0, 4.0);
  auto n = shape.area();
  int total = util_add(1, 2);
  int biggest = util_max(total, 7);
  std::printf("%f %d %d %s\n", n, total, biggest, color_name(Color::Red));
  int scratch = util_add(3, 4);
  std::printf("scratch %d\n", scratch);
  return 0;
}
