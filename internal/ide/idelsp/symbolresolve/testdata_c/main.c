#include <stdio.h>
#include "greeter.h"
#include "utils/text.h"
#include "app/config.h"

int main(void) {
    struct greeter g = { .name = "World" };
    struct config *cfg = config_load("app.cfg");
    printf("%s\n", greet(&g));
    printf("%s\n", slugify("Hello World"));
    config_save(cfg);
    return greeter_count();
}
