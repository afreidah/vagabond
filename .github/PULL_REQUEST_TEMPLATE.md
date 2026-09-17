## Summary

<!-- Brief description of what this PR does and why. -->

## Related Issue

<!-- Link to the GitHub issue, e.g., Closes #123 -->

## Changes

-

## Testing

- [ ] Unit tests pass (`make test`)
- [ ] Linter passes (`make lint`)
- [ ] Formatting applied (`make fmt`)
- [ ] Manual testing performed (describe below if applicable)

## Boundaries

<!--
Confirm the architectural rules in docs/style-guide.md still hold. depguard
enforces these, so an unchecked box here usually means a lint failure too.
-->

- [ ] No provider-specific type appears in a signature the scheduler can see
- [ ] No cloud SDK imported outside `internal/providers/`
- [ ] No database access outside `internal/state/`

## Notes

<!-- Anything reviewers should know: breaking changes, migration steps, etc. -->
