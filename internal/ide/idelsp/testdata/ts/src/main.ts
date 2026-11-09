import { add, Greeter } from "./lib.js";
import { double } from "./util.js";

const g = new Greeter("World");
console.log(g.greet());
const result = add(1, 2);
console.log(double(result));
