#include "greeter.h"

static int count;

/* Private to this translation unit, still a definition. */
static const char *decorate(const char *s) {
    return s;
}

const char *greet(struct greeter *g) {
    count++;
    return decorate(g->name);
}

int greeter_count(void) {
    return count;
}

#define SQUARE(x) ((x) * (x))

enum greeter_kind { FORMAL, CASUAL };

typedef struct greeter greeter_t;

union greeter_payload {
    int code;
    const char *text;
};
