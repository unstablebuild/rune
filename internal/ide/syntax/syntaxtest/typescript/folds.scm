; TypeScript folds for Rune, adapted from the nvim-treesitter ecma and typescript
; queries (Apache-2.0) and tree-sitter-javascript (MIT).
;
; The javascript, typescript and tsx query files share their ECMAScript
; patterns, so change them together. Predicates are limited to #eq?,
; #match? and #any-of?: Rune ignores the others, which would make their
; patterns match unconditionally.

[
  (arguments)
  (array)
  (object)
  (object_pattern)
  (named_imports)
  (export_clause)
  (import_statement)
  (statement_block)
  (class_body)
  (switch_body)
  (switch_case)
  (switch_default)
  (template_string)
  (comment)
] @fold

[
  (interface_body)
  (enum_body)
  (object_type)
  (type_alias_declaration)
] @fold
