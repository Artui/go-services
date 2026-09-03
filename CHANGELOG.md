# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- The kernel: `Spec`, `Registry`, `Register`, `MustRegister`, `Dispatch`,
  `DispatchValue`, `Entry` and `Result`.
- Three validation layers, applied by the kernel on every transport: the
  reflected JSON Schema, an optional `Validate() error` on the input type, and
  `Spec.Permit` for rules that need resolved dependencies.
- `Optional[T]`, which distinguishes an omitted field from a field sent as its
  zero value, and composes with pointers for the explicitly-null case.
- `SchemaFor`, letting a type declare the schema it marshals to. Reflection does
  not consult `json.Marshaler` for arbitrary types, so without this a wrapper
  advertises its own internals.
- `WithAtomic`, which takes a transaction callback so the kernel names no
  database driver. Dependencies resolve inside the transaction.
- `EncodeParams`, schema-directed coercion of flat string parameters, so a query
  string reaches a service as the types its schema declares.
- Framework-agnostic errors: `ErrNotFound`, `ErrConflict`, `ErrPermission` and
  `ValidationError`.
