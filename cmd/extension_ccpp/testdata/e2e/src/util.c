#include "util.h"

#define SQUARE(x) ((x) * (x))
#define CUBE(x)   \
  (SQUARE(x) *    \
   (x))

int util_add(int a, int b) { return a + b; }

int util_max(int a, int b) {
  if (a > b) {
    return a;
  }
  return b;
}

int util_square(int x) { return SQUARE(x); }

int util_cube(int x) { return CUBE(x); }
