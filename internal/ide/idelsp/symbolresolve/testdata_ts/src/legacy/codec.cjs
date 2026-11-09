exports.parse = function (text) {
  return text.split(",");
};

module.exports.format = (parts) => parts.join(",");
