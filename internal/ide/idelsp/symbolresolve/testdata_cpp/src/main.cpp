#include <vector>

#include "geo/shape.hpp"
#include "app/service.hpp"
#include "utils/text.hpp"

// No namespace: this file is qualified by its stem.
int run() {
    geo::Shape s(2.0);
    double a = geo::area(s);
    geo::Point p{1, 2};
    geo::Kind k = geo::Kind::Square;
    std::vector<geo::Shape> shapes = {s, geo::Shape::unit()};
    app::Service svc;
    svc.boot();
    auto slug = utils::slugify("Hello World");
    int dims = geo::kDimensions;
    // Member access on an object is not a qualified reference.
    double m = p.manhattan() + s.area();
    return static_cast<int>(a + m + dims) + static_cast<int>(k) + shapes.size() + slug.size();
}

int main() { return run(); }
