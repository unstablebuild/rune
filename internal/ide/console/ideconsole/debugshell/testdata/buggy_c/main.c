// Fixture program for the lldb-dap debugger e2e suite. sum_to and other
// deliberately reuse the same local names so the variables location list
// can be checked for scope correctness, mirroring the Go/Python/Rust/Zig
// fixtures. Built with `cc -g -O0` so every local keeps its DWARF entry.

#include <stdio.h>

static int sum_to(int n) {
    int total = 0;
    int i = 1;
    while (i <= n) {
        total += i;
        i++;
    }
    return total;
}

static int other(int n) {
    int total = 1;
    int i = 1;
    while (i <= n) {
        total *= i;
        i++;
    }
    return total;
}

int main(void) {
    int result = sum_to(6);
    (void)other(3);
    printf("Sum: %d\n", result);
    return 0;
}
