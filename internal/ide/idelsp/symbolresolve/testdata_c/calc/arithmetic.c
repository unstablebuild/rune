/* helper shares its name with nothing in utils/text.c: stems isolate them. */
static int helper(int x) {
    return x * 2;
}

int add(int a, int b) {
    return helper(a) + b;
}
