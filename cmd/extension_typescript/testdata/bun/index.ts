import { add } from "./math.ts";

const server = Bun.serve({
  port: 0,
  fetch: () => new Response(String(add(1, 2))),
});
console.log(`listening on ${server.port}`);
await server.stop();
