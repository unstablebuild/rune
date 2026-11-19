// Fixture program for the lldb-dap debugger e2e suite. The breakpoint
// lands inside a member function so the stack frame carries the class
// qualifier, and a std::vector local exercises lldb's standard library
// formatters. other reuses the same local names so the variables location
// list can be checked for scope correctness. Built with `c++ -g -O0`.

#include <cstdio>
#include <vector>

class Accumulator {
public:
    explicit Accumulator(int limit) : limit_(limit) {}

    int sum_to() const {
        std::vector<int> seen;
        int total = 0;
        for (int i = 1; i <= limit_; ++i) {
            total += i;
            seen.push_back(i);
        }
        return total;
    }

private:
    int limit_;
};

static int other(int n) {
    std::vector<int> seen;
    int total = 1;
    for (int i = 1; i <= n; ++i) {
        total *= i;
        seen.push_back(i);
    }
    return total;
}

int main() {
    Accumulator acc(6);
    int result = acc.sum_to();
    (void)other(3);
    std::printf("Sum: %d\n", result);
    return 0;
}
