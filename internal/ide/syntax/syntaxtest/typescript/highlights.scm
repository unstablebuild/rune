; TypeScript highlights for Rune, adapted from the nvim-treesitter ecma and typescript
; queries (Apache-2.0) and tree-sitter-javascript (MIT).
;
; The javascript, typescript and tsx query files share their ECMAScript
; patterns, so change them together. Predicates are limited to #eq?,
; #match? and #any-of?: Rune ignores the others, which would make their
; patterns match unconditionally.

; Variables
(identifier) @variable

; Properties
(property_identifier) @property

(shorthand_property_identifier) @property

(private_property_identifier) @property

(object_pattern
  (shorthand_property_identifier_pattern) @variable)

; Naming conventions
((identifier) @type
  (#match? @type "^[A-Z]"))

((identifier) @constant
  (#match? @constant "^_*[A-Z][A-Z0-9_]*$"))

((shorthand_property_identifier) @constant
  (#match? @constant "^_*[A-Z][A-Z0-9_]*$"))

((identifier) @variable.builtin
  (#any-of? @variable.builtin
    "arguments" "module" "exports" "console" "window" "document" "globalThis"
    "process"))

((identifier) @type.builtin
  (#any-of? @type.builtin
    "Object" "Function" "Boolean" "Symbol" "Number" "BigInt" "Math" "Date"
    "String" "RegExp" "Map" "Set" "WeakMap" "WeakSet" "WeakRef" "Promise"
    "Array" "Int8Array" "Uint8Array" "Uint8ClampedArray" "Int16Array"
    "Uint16Array" "Int32Array" "Uint32Array" "Float32Array" "Float64Array"
    "BigInt64Array" "BigUint64Array" "ArrayBuffer" "SharedArrayBuffer"
    "DataView" "JSON" "Reflect" "Proxy" "Intl" "Error" "AggregateError"
    "EvalError" "RangeError" "ReferenceError" "SyntaxError" "TypeError"
    "URIError"))

(statement_identifier) @label

; Function and method definitions
(function_expression
  name: (identifier) @function)

(function_declaration
  name: (identifier) @function)

(generator_function
  name: (identifier) @function)

(generator_function_declaration
  name: (identifier) @function)

(method_definition
  name: [
    (property_identifier)
    (private_property_identifier)
  ] @function.method)

(method_definition
  name: (property_identifier) @constructor
  (#eq? @constructor "constructor"))

(pair
  key: (property_identifier) @function.method
  value: [
    (function_expression)
    (arrow_function)
  ])

(assignment_expression
  left: (member_expression
    property: (property_identifier) @function.method)
  right: [
    (function_expression)
    (arrow_function)
  ])

(variable_declarator
  name: (identifier) @function
  value: [
    (function_expression)
    (arrow_function)
  ])

(assignment_expression
  left: (identifier) @function
  right: [
    (function_expression)
    (arrow_function)
  ])

; Function and method calls
(call_expression
  function: (identifier) @function.call)

(call_expression
  function: (member_expression
    property: [
      (property_identifier)
      (private_property_identifier)
    ] @function.method.call))

(new_expression
  constructor: (identifier) @constructor)

((identifier) @function.builtin
  (#any-of? @function.builtin
    "eval" "isFinite" "isNaN" "parseFloat" "parseInt" "decodeURI"
    "decodeURIComponent" "encodeURI" "encodeURIComponent" "require"
    "structuredClone" "queueMicrotask" "setTimeout" "setInterval"
    "clearTimeout" "clearInterval"))

; Decorators
(decorator
  "@" @attribute
  (identifier) @attribute)

(decorator
  "@" @attribute
  (call_expression
    (identifier) @attribute))

(decorator
  "@" @attribute
  (member_expression
    (property_identifier) @attribute))

(decorator
  "@" @attribute
  (call_expression
    (member_expression
      (property_identifier) @attribute)))

; Literals
[
  (this)
  (super)
] @variable.builtin

[
  (true)
  (false)
] @boolean

[
  (null)
  (undefined)
] @constant.builtin

((identifier) @number
  (#any-of? @number "NaN" "Infinity"))

(number) @number

(string) @string

(template_string) @string

(escape_sequence) @string.escape

(regex) @string.regexp

(template_substitution
  [
    "${"
    "}"
  ] @punctuation.special) @embedded

(hash_bang_line) @keyword.directive

((string_fragment) @keyword.directive
  (#eq? @keyword.directive "use strict"))

(comment) @comment

((comment) @comment.documentation
  (#match? @comment.documentation "^/\\*\\*[^*]"))

; Punctuation
[
  ";"
  "."
  ","
  ":"
  (optional_chain)
] @punctuation.delimiter

[
  "("
  ")"
  "["
  "]"
  "{"
  "}"
] @punctuation.bracket

[
  "--"
  "-"
  "-="
  "&&"
  "+"
  "++"
  "+="
  "&="
  "/="
  "**="
  "<<="
  "<"
  "<="
  "<<"
  "="
  "=="
  "==="
  "!="
  "!=="
  "=>"
  ">"
  ">="
  ">>"
  "||"
  "%"
  "%="
  "*"
  "**"
  ">>>"
  "&"
  "|"
  "^"
  "??"
  "*="
  ">>="
  ">>>="
  "^="
  "|="
  "&&="
  "||="
  "??="
  "..."
  "!"
  "~"
] @operator

(binary_expression
  "/" @operator)

(ternary_expression
  [
    "?"
    ":"
  ] @operator)

; Imports
(namespace_import
  (identifier) @module)

(namespace_export
  (identifier) @module)

; Keywords
[
  "if"
  "else"
  "switch"
  "case"
] @keyword.conditional

[
  "import"
  "from"
  "as"
  "export"
] @keyword.import

[
  "for"
  "of"
  "do"
  "while"
  "continue"
] @keyword.repeat

[
  "break"
  "class"
  "const"
  "debugger"
  "default"
  "extends"
  "get"
  "let"
  "set"
  "static"
  "target"
  "var"
  "with"
] @keyword

[
  "async"
  "await"
] @keyword.coroutine

[
  "return"
  "yield"
] @keyword.return

"function" @keyword.function

[
  "new"
  "delete"
  "in"
  "instanceof"
  "typeof"
  "void"
] @keyword.operator

[
  "throw"
  "try"
  "catch"
  "finally"
] @keyword.exception

; TypeScript
(type_identifier) @type

(predefined_type) @type.builtin

(import_statement
  "type"
  (import_clause
    (named_imports
      (import_specifier
        name: (identifier) @type))))

(template_literal_type) @string

(non_null_expression
  "!" @operator)

(type_arguments
  [
    "<"
    ">"
  ] @punctuation.bracket)

(type_parameters
  [
    "<"
    ">"
  ] @punctuation.bracket)

(union_type
  "|" @punctuation.delimiter)

(intersection_type
  "&" @punctuation.delimiter)

(conditional_type
  [
    "?"
    ":"
  ] @operator)

; Parameters
(required_parameter
  pattern: (identifier) @variable.parameter)

(optional_parameter
  pattern: (identifier) @variable.parameter)

(required_parameter
  (rest_pattern
    (identifier) @variable.parameter))

(required_parameter
  (object_pattern
    (shorthand_property_identifier_pattern) @variable.parameter))

(required_parameter
  (object_pattern
    (pair_pattern
      value: (identifier) @variable.parameter)))

(required_parameter
  (array_pattern
    (identifier) @variable.parameter))

(arrow_function
  parameter: (identifier) @variable.parameter)

; Declarations
(ambient_declaration
  "global" @module)

(function_signature
  name: (identifier) @function)

(method_signature
  name: (_) @function.method)

(abstract_method_signature
  name: (property_identifier) @function.method)

(property_signature
  name: (property_identifier) @function.method
  type: (type_annotation
    (function_type)))

(internal_module
  name: (identifier) @module)

; Keywords
"require" @keyword.import

[
  "declare"
  "implements"
  "type"
  "override"
  "module"
  "asserts"
  "infer"
  "is"
  "namespace"
  "interface"
  "enum"
  "abstract"
  "private"
  "protected"
  "public"
  "readonly"
  "accessor"
] @keyword

[
  "keyof"
  "satisfies"
] @keyword.operator

(as_expression
  "as" @keyword.operator)
