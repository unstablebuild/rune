; JavaScript locals for Rune, adapted from the nvim-treesitter ecma and jsx
; queries (Apache-2.0) and tree-sitter-javascript (MIT).
;
; The javascript, typescript and tsx query files share their ECMAScript
; patterns, so change them together. Predicates are limited to #eq?,
; #match? and #any-of?: Rune ignores the others, which would make their
; patterns match unconditionally.

; Scopes
[
  (program)
  (statement_block)
  (function_expression)
  (function_declaration)
  (generator_function)
  (generator_function_declaration)
  (arrow_function)
  (method_definition)
  (class)
  (class_declaration)
  (for_statement)
  (for_in_statement)
  (catch_clause)
] @local.scope

; Imports
(import_clause
  (identifier) @local.definition.import)

(import_specifier
  !alias
  name: (identifier) @local.definition.import)

(import_specifier
  alias: (identifier) @local.definition.import)

(namespace_import
  (identifier) @local.definition.import)

; Functions
(function_declaration
  name: (identifier) @local.definition.function)

(generator_function_declaration
  name: (identifier) @local.definition.function)

(function_expression
  name: (identifier) @local.definition.function)

(generator_function
  name: (identifier) @local.definition.function)

(variable_declarator
  name: (identifier) @local.definition.function
  value: [
    (arrow_function)
    (function_expression)
    (generator_function)
  ])

(assignment_expression
  left: (member_expression
    property: (property_identifier) @local.definition.function)
  right: [
    (arrow_function)
    (function_expression)
  ])

; Methods
(method_definition
  name: [
    (property_identifier)
    (private_property_identifier)
  ] @local.definition.method)

(pair
  key: (property_identifier) @local.definition.method
  value: [
    (arrow_function)
    (function_expression)
  ])

; Variables
(variable_declarator
  name: (identifier) @local.definition.var)

(variable_declarator
  name: (object_pattern
    (shorthand_property_identifier_pattern) @local.definition.var))

(variable_declarator
  name: (object_pattern
    (pair_pattern
      value: (identifier) @local.definition.var)))

(variable_declarator
  name: (array_pattern
    (identifier) @local.definition.var))

(for_in_statement
  left: (identifier) @local.definition.var)

(catch_clause
  parameter: (identifier) @local.definition.var)

(arrow_function
  parameter: (identifier) @local.definition.parameter)

; References
(identifier) @local.reference

(shorthand_property_identifier) @local.reference

(property_identifier) @local.reference

; Classes
(class_declaration
  name: (identifier) @local.definition.type)

(class
  name: (identifier) @local.definition.type)

(field_definition
  property: [
    (property_identifier)
    (private_property_identifier)
  ] @local.definition.field)

; Parameters
(formal_parameters
  (identifier) @local.definition.parameter)

(formal_parameters
  (assignment_pattern
    left: (identifier) @local.definition.parameter))

(formal_parameters
  (rest_pattern
    (identifier) @local.definition.parameter))

(formal_parameters
  (object_pattern
    (shorthand_property_identifier_pattern) @local.definition.parameter))

(formal_parameters
  (object_pattern
    (pair_pattern
      value: (identifier) @local.definition.parameter)))

(formal_parameters
  (array_pattern
    (identifier) @local.definition.parameter))
