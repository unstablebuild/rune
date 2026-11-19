; C++ highlights for Rune, adapted from tree-sitter-c and tree-sitter-cpp queries/highlights.scm
; (MIT). Predicates are limited to #eq?, #match? and #any-of?: Rune ignores
; the others, which would make their patterns match unconditionally.

(identifier) @variable

((identifier) @constant
  (#match? @constant "^[A-Z][A-Z\\d_]*$"))

[
  "break"
  "case"
  "const"
  "continue"
  "default"
  "do"
  "else"
  "enum"
  "extern"
  "for"
  "goto"
  "if"
  "inline"
  "register"
  "return"
  "sizeof"
  "static"
  "struct"
  "switch"
  "typedef"
  "union"
  "volatile"
  "while"
] @keyword

(storage_class_specifier) @keyword
(type_qualifier) @keyword

[
  "#define"
  "#elif"
  "#else"
  "#endif"
  "#if"
  "#ifdef"
  "#ifndef"
  "#include"
  (preproc_directive)
] @keyword

[
  "--"
  "-"
  "-="
  "->"
  "="
  "!"
  "!="
  "*"
  "*="
  "/"
  "/="
  "%"
  "%="
  "&"
  "&="
  "&&"
  "|"
  "|="
  "||"
  "^"
  "^="
  "~"
  "+"
  "++"
  "+="
  "<"
  "<="
  "<<"
  "<<="
  "=="
  ">"
  ">="
  ">>"
  ">>="
  "?"
] @operator

[
  "."
  ";"
  ","
  ":"
] @delimiter

(string_literal) @string
(system_lib_string) @string
(escape_sequence) @string.escape

(null) @constant
[
  (true)
  (false)
] @constant.builtin
(number_literal) @number
(char_literal) @number

(field_identifier) @property
(statement_identifier) @label
(type_identifier) @type
(primitive_type) @type
(sized_type_specifier) @type

(call_expression
  function: (identifier) @function)
(call_expression
  function: (field_expression
    field: (field_identifier) @function))
(function_declarator
  declarator: (identifier) @function)
(preproc_function_def
  name: (identifier) @function.special)

(comment) @comment

; C++

(call_expression
  function: (qualified_identifier
    name: (identifier) @function))

(call_expression
  function: (template_function
    name: (identifier) @function))

(call_expression
  function: (field_expression
    field: (template_method
      name: (field_identifier) @function)))

(function_declarator
  declarator: (qualified_identifier
    name: (identifier) @function))

(function_declarator
  declarator: (template_function
    name: (identifier) @function))

(function_declarator
  declarator: (template_method
    name: (field_identifier) @function))

(function_declarator
  declarator: (field_identifier) @function)

(function_declarator
  declarator: (destructor_name) @function)

(function_declarator
  declarator: (operator_name) @function)

((namespace_identifier) @type
  (#match? @type "^[A-Z]"))

(auto) @type

(this) @variable.builtin

(null
  "nullptr" @constant)

(module_name
  (identifier) @module)

[
  "catch"
  "class"
  "co_await"
  "co_return"
  "co_yield"
  "concept"
  "consteval"
  "constexpr"
  "constinit"
  "decltype"
  "delete"
  "explicit"
  "export"
  "final"
  "friend"
  "import"
  "module"
  "mutable"
  "namespace"
  "new"
  "noexcept"
  "operator"
  "override"
  "private"
  "protected"
  "public"
  "requires"
  "static_assert"
  "template"
  "throw"
  "try"
  "typename"
  "using"
  "virtual"
] @keyword

[
  "::"
] @delimiter

(raw_string_literal) @string
