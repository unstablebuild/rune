// Incomplete code: the definitions before the error still index.
namespace geo {

class Widget {
public:
    int size() const { return n_; }
private:
    int n_;
};

}  // namespace geo

int broken(int x) {
    if (x > 0 {
        return
