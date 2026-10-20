---
applyTo: "**/*.md"
---

# Documentation instructions

- Keep shared architecture, installation, operations, testing and roadmap in
  the workspace docs/ directory. Component README files link there.
- Keep helper API/native contracts in xray-core-helper/docs/.
- Separate implemented behavior, planned work and dated acceptance evidence.
- Do not add a separate phase plan or completion report for each task; update
  the relevant canonical document. Git retains historical documents.
- Commands must be copy-pasteable, scoped to this project and free of secrets.
- Mark host-mutating commands clearly and pair them with a verification step.
- Update README when implemented behavior or runnable commands change.
