#include "utils/text.hpp"

namespace utils {

static std::string private_helper(std::string s) { return s; }

std::string slugify(const std::string &s) { return private_helper(s); }

}  // namespace utils
