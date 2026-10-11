import * as requests from "./requests.js";

export function fetchAll(urls) {
  return urls.map((url) => requests.get(url));
}
