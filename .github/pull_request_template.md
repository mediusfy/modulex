## Summary

<!-- Briefly describe the change and why it is needed. -->

## Related tickets

<!-- Link to related Jira or GitHub issues. -->

## Checklist

<!-- Criteria: .ai/guidance/testing.md, "Merge and verification criteria". Tick only what you checked; for anything not applicable, say why. -->

- [ ] `make test-arch`, `make build`, `make lint`, and `make test` pass locally.
- [ ] I have added or updated tests for the changed code.
- [ ] I have updated the documentation and `CHANGELOG.md` if this change is user-facing.
- [ ] **CI status:** CI and PR Review are green on the latest commit.
- [ ] **Sonar analysis:** the SonarCloud quality gate passes and new findings are addressed.
- [ ] **Integration and end to end:** RabbitMQ integration tests, `make check-nested-modules` and editor plugin tests pass where the change touches them (or not required).
- [ ] **Coverage and gaps:** coverage of the changed code was checked, with no missing branches or edge cases left unexplained.

## AI Context & Rules Update

<!-- One to five lines for coding agents: a new convention, an architectural shift, a rule that changed or a gotcha found. Write "None" if nothing changed. When this PR merges, this section is copied into .ai/recent_context.md. -->

None
