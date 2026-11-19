// Same namespace as app/config.cpp: both define load.
namespace config {
int load(const char *path) { (void)path; return 0; }
void reset() {}
}  // namespace config
