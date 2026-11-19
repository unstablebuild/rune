#ifndef GREETER_H
#define GREETER_H

/* Declared here, defined in greeter.c: both files share the stem. */
struct greeter {
    const char *name;
};

const char *greet(struct greeter *g);
int greeter_count(void);

#define GREETING_MAX 64

#endif
