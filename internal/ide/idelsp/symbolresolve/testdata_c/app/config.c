#include "config.h"

struct config {
    int verbose;
};

struct config *config_load(const char *path) {
    (void)path;
    return 0;
}

void config_save(struct config *cfg) {
    (void)cfg;
}
