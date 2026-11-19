#ifndef APP_CONFIG_H
#define APP_CONFIG_H

struct config;

struct config *config_load(const char *path);
void config_save(struct config *cfg);

#endif
