# Design Decisions

How value-laden design forks are weighed in this project.

## Weighing Design Forks

(extension point: `design-fork-adjudication`)

lookout is a general-purpose tool: it reads activity for arbitrary users whose repositories, trackers, and ways of grouping work its author will never see. So when a design fork is value-laden — what goes in the configuration file, what an input contract carries, which default to ship, how to shape the readout — weigh it by **what most open-source users of a tool like this would want**, not by what fits the author's own first use case. A fork resolved from one user's setup fails the general-purpose thesis the whole tool rests on.

This lens is the value such a fork is weighed on, not its settlement. It is the deliberate alternative to reaching for the simplest, least-code, or most-general option: ask what the broad population of users wants. Where the lens and every other axis the fork touches — correctness, the input contracts' stability, the cost of changing course later — agree and the choice can be undone, settle it and say which axes agreed and why it can be undone; otherwise present the grounded finding with a lean and leave the call to the maintainer. A published schema version is read by suppliers this project does not control, so a contract fork is rarely one that can be undone cheaply.

**The specific pull to resist here** is encoding one tracker's or one organization's shape into the contracts. A supplied item carries tracker-specific detail in `attrs`, typed links, and free-form state — not in fields named after one tracker's concepts. What one organization calls a sprint, an estimate, or a component belongs in that user's configuration and supplier, where it can differ per user without a schema change.

**How to apply:**

- Ground the call in the actual public landscape — survey how comparable real-world projects handle it — rather than extrapolating from the author's own conventions. A proportionate survey is enough; size the research to the decision.
- Present the grounded finding before recommending, so the reasoning is visible and redirectable.
- When new landscape data reshapes a decision, re-examine already-shipped sibling decisions for whether the finding transfers — but verify it actually does, since different semantics can keep the original choice correct.

This lens applies to product- and convention-level forks. Purely mechanical or internal-engineering choices (which helper to reuse, naming, internal structure) stay ordinary engineering judgment.
