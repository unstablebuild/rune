import * as geometry from "./geometry";
import * as service from "./app/service";
import * as text from "./utils/text";
import requests = require("./vendor/requests");
import { slugify } from "./utils";

export function main(): void {
  // Qualified references resolve to this call site via the reference
  // phase; console is not an imported module.
  const shape: geometry.Shape = new geometry.Shape(3);
  const total = geometry.area(shape);
  const response = requests.get("https://example.com");
  const svc = new service.Service();
  console.log(total, response, svc, text.slugify("Hello World"), slugify("x"));
}
