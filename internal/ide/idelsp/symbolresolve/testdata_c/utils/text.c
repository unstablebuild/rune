#include "text.h"

static char *private_helper(char *s) {
    return s;
}

char *slugify(const char *s) {
    return private_helper((char *)s);
}
