const requests = require("./requests.js");

function load(url) {
  return new requests.Response(url);
}

module.exports = { load };
