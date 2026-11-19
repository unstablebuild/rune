#pragma once

#include <string>

class Greeter {
 public:
  explicit Greeter(std::string name);
  std::string greet() const;
  int count() const;

 private:
  std::string name_;
  int count_;
};
