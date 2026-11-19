; C locals for Rune, adapted from nvim-treesitter's queries/c/locals.scm
; (Apache-2.0). Only struct, union and enum specifiers with a body count
; as type definitions: `struct point *p;` is a use of the type, not a
; declaration of it. Function-like macros are indexed as functions and
; object-like macros as variables so they resolve across files.

; Functions
(function_declarator
  declarator: (identifier) @local.definition.function)

(function_declarator
  declarator: (parenthesized_declarator
    (pointer_declarator
      declarator: (identifier) @local.definition.var)))

(preproc_function_def
  name: (identifier) @local.definition.function) @local.scope

(preproc_def
  name: (identifier) @local.definition.var)

(preproc_params
  (identifier) @local.definition.parameter)

; Variables
(pointer_declarator
  declarator: (identifier) @local.definition.var)

(parameter_declaration
  declarator: (identifier) @local.definition.parameter)

(init_declarator
  declarator: (identifier) @local.definition.var)

(array_declarator
  declarator: (identifier) @local.definition.var)

(declaration
  declarator: (identifier) @local.definition.var)

(enumerator
  name: (identifier) @local.definition.var)

; Types
(struct_specifier
  name: (type_identifier) @local.definition.type
  body: (field_declaration_list))

(union_specifier
  name: (type_identifier) @local.definition.type
  body: (field_declaration_list))

(enum_specifier
  name: (type_identifier) @local.definition.type
  body: (enumerator_list))

(type_definition
  declarator: (type_identifier) @local.definition.type)

(field_declaration
  declarator: (field_identifier) @local.definition.field)

(field_declaration
  declarator: (pointer_declarator
    declarator: (field_identifier) @local.definition.field))

(field_declaration
  declarator: (array_declarator
    declarator: (field_identifier) @local.definition.field))

; goto
(labeled_statement
  (statement_identifier) @local.definition)

; References
(identifier) @local.reference

((field_identifier) @local.reference
  (#set! reference.kind "field"))

((type_identifier) @local.reference
  (#set! reference.kind "type"))

(goto_statement
  (statement_identifier) @local.reference)

; Scopes
[
  (translation_unit)
  (function_definition)
  (compound_statement)
  (for_statement)
  (if_statement)
  (while_statement)
  (do_statement)
  (switch_statement)
  (struct_specifier)
  (union_specifier)
  (enum_specifier)
] @local.scope
