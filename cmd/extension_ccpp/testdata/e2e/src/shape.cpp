#include "shape.hpp"

namespace geo {

Shape::Shape(double width, double height) : width_(width), height_(height) {}

double Shape::area() const { return width_ * height_; }

double Shape::perimeter() const { return 2 * (width_ + height_); }

}  // namespace geo
