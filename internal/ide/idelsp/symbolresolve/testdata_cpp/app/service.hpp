#pragma once

namespace app {

class Service {
public:
    void boot();
    void stop() {}
};

// Nested namespace: the file's package is the first one it declares.
namespace detail {
int helper();
}  // namespace detail

}  // namespace app
