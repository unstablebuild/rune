#include "app/service.hpp"
#include "utils/text.hpp"

namespace app {

void Service::boot() { utils::slugify("boot"); }

int detail::helper() { return 1; }

}  // namespace app
