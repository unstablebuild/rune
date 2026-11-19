#include "geo/shape.hpp"

namespace geo {

Shape::Shape(double side) : side_(side) {}

double Shape::area() const { return side_ * side_; }

double Shape::perimeter() const { return 4 * side_; }

Shape Shape::unit() { return Shape(1.0); }

template <typename T> T Shape::scale(T factor) const {
    return factor * static_cast<T>(side_);
}

double area(const Shape &s) { return s.area(); }

namespace {
// Anonymous namespace: not observable, still a definition.
double hidden(double x) { return x; }
}  // namespace

}  // namespace geo
