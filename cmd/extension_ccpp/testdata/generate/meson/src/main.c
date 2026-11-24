#ifndef FROM_MESON
#error "compiled without the meson.build flags"
#endif
#include "util.h"

int main(void) { return util_answer() - 42; }