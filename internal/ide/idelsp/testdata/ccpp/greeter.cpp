#include "greeter.hpp"

Greeter::Greeter(std::string name) : name_(std::move(name)), count_(0) {}

std::string Greeter::greet() const { return "Hello, " + name_ + "!"; }

int Greeter::count() const { return count_; }
