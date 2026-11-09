export class Response {
  json() {
    return {};
  }
}

export function get(url) {
  return new Response(url);
}

function retry() {}
