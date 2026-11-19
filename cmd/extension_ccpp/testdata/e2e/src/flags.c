#ifndef FROM_COMPDB
#error "built without the pinned compilation database"
#endif

int flags_value(void) { return FROM_COMPDB; }
