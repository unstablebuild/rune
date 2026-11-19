; C++ locals for Rune, adapted from nvim-treesitter's queries/c and queries/cpp locals.scm
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

; C++

; Parameters
(variadic_parameter_declaration
  declarator: (variadic_declarator
    (identifier) @local.definition.parameter))

(optional_parameter_declaration
  declarator: (identifier) @local.definition.parameter)

; Variables
(reference_declarator
  (identifier) @local.definition.var)

(variadic_declarator
  (identifier) @local.definition.var)

(structured_binding_declarator
  (identifier) @local.definition.var)

(for_range_loop
  declarator: (identifier) @local.definition.var)

; Types
(class_specifier
  name: (type_identifier) @local.definition.type
  body: (field_declaration_list))

(struct_specifier
  name: (qualified_identifier
    name: (type_identifier) @local.definition.type)
  body: (field_declaration_list))

(class_specifier
  name: (qualified_identifier
    name: (type_identifier) @local.definition.type)
  body: (field_declaration_list))

(concept_definition
  name: (identifier) @local.definition.type)

(alias_declaration
  name: (type_identifier) @local.definition.type)

(type_parameter_declaration
  (type_identifier) @local.definition.type)

; Namespaces
(namespace_definition
  name: (namespace_identifier) @local.definition.namespace
  body: (_) @local.scope)

(namespace_definition
  name: (nested_namespace_specifier) @local.definition.namespace
  body: (_) @local.scope)

((namespace_identifier) @local.reference
  (#set! reference.kind "namespace"))

; Functions and methods. An out-of-line `T::m` definition is a function
; here; the resolver pairs it with its class through its own query.
(function_declarator
  declarator: (qualified_identifier
    name: (identifier) @local.definition.function))

(function_declarator
  declarator: (qualified_identifier
    name: (qualified_identifier
      name: (identifier) @local.definition.function)))

(function_declarator
  declarator: (template_function
    name: (identifier) @local.definition.function))

(function_declarator
  declarator: (field_identifier) @local.definition.method)

(function_declarator
  declarator: (template_method
    name: (field_identifier) @local.definition.method))

(function_declarator
  declarator: (destructor_name) @local.definition.method)

; Scopes
[
  (translation_unit)
  (function_definition)
  (compound_statement)
  (for_statement)
  (for_range_loop)
  (if_statement)
  (while_statement)
  (do_statement)
  (switch_statement)
  (struct_specifier)
  (union_specifier)
  (enum_specifier)
  (class_specifier)
  (template_declaration)
  (lambda_expression)
  (catch_clause)
  (requires_expression)
] @local.scope

(try_statement
  body: (_) @local.scope)
