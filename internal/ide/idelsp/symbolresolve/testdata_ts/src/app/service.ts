import * as text from "../utils/text";

export class Service {
  start(): string {
    return text.slugify("service");
  }
}

export function boot(): Service {
  return new Service();
}
