/* Same stem as app/config.c: both define config_load. */
struct config;

struct config *config_load(const char *path) {
    (void)path;
    return 0;
}

void config_reset(void) {}
