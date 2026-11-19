#pragma once

namespace geo {

class Shape {
 public:
  Shape(double width, double height);
  double area() const;
  double perimeter() const;

 private:
  double width_;
  double height_;
};

}  // namespace geo
