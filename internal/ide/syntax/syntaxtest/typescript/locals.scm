; TypeScript locals for Rune, adapted from the nvim-treesitter ecma and typescript
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

; Scopes
[
  (interface_declaration)
  (abstract_class_declaration)
  (internal_module)
  (module)
] @local.scope

; Types
(class_declaration
  name: (type_identifier) @local.definition.type)

(class
  name: (type_identifier) @local.definition.type)

(abstract_class_declaration
  name: (type_identifier) @local.definition.type)

(interface_declaration
  name: (type_identifier) @local.definition.type)

(type_alias_declaration
  name: (type_identifier) @local.definition.type)

(enum_declaration
  name: (identifier) @local.definition.type)

; Namespaces
(internal_module
  name: (identifier) @local.definition.namespace)

(module
  name: (identifier) @local.definition.namespace)

; Overloads and ambient functions
(function_signature
  name: (identifier) @local.definition.function)

; Members
(method_signature
  name: (property_identifier) @local.definition.method)

(abstract_method_signature
  name: (property_identifier) @local.definition.method)

(public_field_definition
  name: [
    (property_identifier)
    (private_property_identifier)
  ] @local.definition.field)

(interface_body
  (property_signature
    name: (property_identifier) @local.definition.field))

(type_alias_declaration
  value: (object_type
    (property_signature
      name: (property_identifier) @local.definition.field)))

(enum_body
  name: (property_identifier) @local.definition.field)

(enum_assignment
  name: (property_identifier) @local.definition.field)

; Parameters
(required_parameter
  pattern: (identifier) @local.definition.parameter)

(optional_parameter
  pattern: (identifier) @local.definition.parameter)

(required_parameter
  pattern: (rest_pattern
    (identifier) @local.definition.parameter))

(required_parameter
  pattern: (object_pattern
    (shorthand_property_identifier_pattern) @local.definition.parameter))

(required_parameter
  pattern: (object_pattern
    (pair_pattern
      value: (identifier) @local.definition.parameter)))

(required_parameter
  pattern: (array_pattern
    (identifier) @local.definition.parameter))

(type_parameter
  name: (type_identifier) @local.definition.parameter)

; References
(type_identifier) @local.reference
