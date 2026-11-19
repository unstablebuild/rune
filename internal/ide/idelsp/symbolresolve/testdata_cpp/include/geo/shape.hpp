#pragma once

namespace geo {

// Declared here, defined in src/shape.cpp.
class Shape {
public:
    explicit Shape(double side);
    double area() const;
    double perimeter() const;
    static Shape unit();
    template <typename T> T scale(T factor) const;

private:
    double side_;
};

double area(const Shape &s);

struct Point {
    int x;
    int y;
    int manhattan() const { return x + y; }
};

enum class Kind { Square, Circle };

using Side = double;

constexpr int kDimensions = 2;

}  // namespace geo
