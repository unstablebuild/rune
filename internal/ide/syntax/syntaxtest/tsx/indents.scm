; TSX indents for Rune, adapted from the nvim-treesitter ecma, typescript and jsx
; queries (Apache-2.0) and tree-sitter-javascript (MIT).
;
; The javascript, typescript and tsx query files share their ECMAScript
; patterns, so change them together. Predicates are limited to #eq?,
; #match? and #any-of?: Rune ignores the others, which would make their
; patterns match unconditionally.

[
  (arguments)
  (array)
  (binary_expression)
  (class_body)
  (export_clause)
  (formal_parameters)
  (named_imports)
  (object)
  (object_pattern)
  (array_pattern)
  (parenthesized_expression)
  (return_statement)
  (statement_block)
  (switch_body)
  (switch_case)
  (switch_default)
  (template_substitution)
  (ternary_expression)
  (variable_declarator)
  (assignment_expression)
  (member_expression)
] @indent.begin

(arguments
  (call_expression) @indent.begin)

(binary_expression
  (call_expression) @indent.begin)

(expression_statement
  (call_expression) @indent.begin)

(arrow_function
  body: (expression)) @indent.begin

(if_statement
  consequence: (expression_statement)) @indent.begin

[
  ")"
  "}"
  "]"
] @indent.branch

(arguments
  ")" @indent.end)

[
  "}"
  "]"
] @indent.end

(template_string) @indent.ignore

[
  (comment)
  (ERROR)
] @indent.auto

[
  (enum_body)
  (interface_body)
  (object_type)
  (type_arguments)
  (type_parameters)
] @indent.begin

[
  (jsx_element)
  (jsx_self_closing_element)
  (jsx_expression)
  (jsx_opening_element)
] @indent.begin

(jsx_closing_element
  ">" @indent.end)

(jsx_self_closing_element
  "/>" @indent.end)

[
  (jsx_closing_element)
  ">"
  "/>"
] @indent.branch
