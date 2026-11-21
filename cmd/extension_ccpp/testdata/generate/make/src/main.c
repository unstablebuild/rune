#ifndef FROM_MAKE
#error "compiled without the Makefile flags"
#endif
#include "util.h"

int main(void) { return util_answer() - 42; }
