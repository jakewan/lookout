# Workstream Dashboard — Vision

What a graphical view of one person's concurrent workstreams has to show, why it has to be graphical, the design decisions already settled, and the questions they leave open.

lookout is the data layer of this dashboard: it gathers the activity, groups it into workstreams, and reports the health of every input. The renderer that draws the view is not chosen, and this document is where its requirements are kept until it is.

## Purpose

**Throughput is a lever, not a constant.** Concurrent AI-assisted workstreams — several agent sessions, branches and reviews in flight at once — are already part of the daily process. Used with better judgment, and kept from going stale, they raise how much work completes. A resolved-work figure measured over past iterations describes the past process, not a ceiling on the next one.

**The unit is the workstream, not the ticket.** A workstream spans whatever carries one line of work: agent sessions, branches, pull requests, tracker tickets, GitHub issues. Motion is recorded across all of them, and the links between them are often prose or absent, so a view built from any one source cannot tell an idle workstream from an active one.

**Graphical is a requirement.** Juggling concurrent workstreams means seeing them side by side, and text — an agent's report, a generated planning page, a GitHub listing — shows them one after another. Where a text view of this need falls short, it is the sequential output that fails, not the analysis behind it.

**Growth happens after admission.** Work commonly enters as a small exploratory item and grows into a project without a planning step between. The dashboard does not fix that; it makes the growth visible so the tradeoff is made with the facts in view.

## What the dashboard shows

1. **The workstreams**, with key statistics for each — above all, when each was last touched.
2. **A prominent ranking.** It answers "is what I'm doing right now okay, given what I've already done today?" and suggests where attention goes early in the day, without hiding lower-ranked workstreams.
3. **Critical items the tracker does not account for:**
   - questions awaiting an answer, in chat or the tracker
   - time-critical tasks, tracked or not
   - stop-the-line signals in a critical area — not every one stops every line
4. **A drill-down into each workstream**, including a map of how its tickets and issues relate.
5. **A "where I left off" cue for each workstream**, recorded when it is paused and shown on return.
6. **Adaptation to the inputs available.** A setting whose inputs include a tracker and chat shows them; a setting with neither shows no gap where they would be.

Three invariants carry over from the problem this answers:

- **A day with something in it looks different from a day without.** A surface that always looks the same becomes wallpaper, and wallpaper also creates a false impression of coverage.
- **Fail visibly.** An input that is broken, or reading less than it should, is shown as such — never as an empty, complete-looking picture.
- **Write nothing team-visible.** This is a read. Filing, commenting, assigning and transitioning stay with the user.

## Settled decisions

**Ranking comes from both stated order and observed staleness.** The user sets the order; observed time-since-touched flags any workstream falling behind it. Either alone fails: observed-only promotes deliberately parked work, and stated-only never notices drift. This sets declared intent against revealed behavior within a day's workstreams.

**A declarative configuration file per setting.** One file for each setting — work, personal, or any other — declares:

- the workstreams, and which sessions, branches, pull requests, tickets and issues belong to each
- the stated order the ranking reads
- which inputs the setting expects, so the dashboard can tell an absent input from a broken one

Where several settings are configured, one is selected explicitly, and two settings are never merged into one view.

The configuration file owns the stated order; any planning page refers to it rather than holding a second copy. Discovery proposes changes to the file and never makes them: activity matching no workstream appears in its own group, with a one-step adopt-or-dismiss, so the configuration's own staleness is visible on the dashboard and its upkeep is a decision at the moment something new appears, not a separate chore. The configuration describes the user's own systems, so it lives on the user's machine, never in lookout's repository.

**Every forge-backed input is fenced by configured owners.** A setting names the owners whose repositories, pull requests and sessions it admits, and nothing else enters its readout. One account's credentials often reach several contexts' work, and the fence keeps them from pulling one context's work into another's view.

**Genericity by configuration.** lookout serves users whose repositories, trackers and ways of grouping work its authors never see. Whatever one user or one organization does differently — which owners count, which inputs a setting expects, what a tracker calls a sprint — is configuration, never code.

**Data layer first; the renderer stays replaceable.** The part that has to be built whatever draws it is the configuration, one adapter per input, and a health record per input. A tool supplies reduced facts, and a surface renders them. The renderer is chosen later, informed by what the data layer turns out to need.

**Supplied files for sources lookout does not read itself.** lookout never talks to an issue tracker: a tracker's data reaches it as a file an outside supplier writes, already reduced, in a versioned contract. A supplier knows nothing about workstreams, and lookout knows nothing about how the supplier reached its source, so either side can change without the other. Which other sources arrive this way — dated obligations among them — is open.

**A health record per input.** Each adapter reports whether its input is healthy, in error, absent by design, or degraded, with when it was fetched and how many records it held. lookout computes health; an input's own report can lower it but never raise it.

The surface shows each input in one of these states: hidden (not expected in this setting), healthy, error, and degraded. An expected input that is missing is an error, and so is a supplier that reports its own error. Degraded is a reading that succeeded but cannot be trusted as complete:

- a call that came back empty where records are expected, or missing fields it should carry
- a supplied file that fails its schema
- a supplied file older than the age its setting allows
- a supplier whose last attempt failed after its last success
- a supplier that reports itself degraded, or names fields it could not fill

Degraded is never rendered as a quiet "no data". No off-the-shelf dashboard draws this line by itself; the pattern is Prometheus's `absent()` — check for missing data only where data is expected (<https://prometheus.io/docs/prometheus/latest/querying/functions/>) — and the configuration is what states the expectation.

## Design principles, with their sources

Each principle below cites its source. Where a claim was taken from a secondary source rather than the original, it says so.

### Layout

- **Overview first, then detail on demand.** Shneiderman's mantra — "overview first, zoom and filter, then details on demand" — maps onto items 1–2 as the overview and item 4 as the detail. The ticket map is his *Relate* task, and he notes that choosing which relationship to show is still a design challenge. His *History* task means the history of the user's own actions (undo, replay), not change in the data, and is not a citation for showing change over time. Shneiderman, "The Eyes Have It," 1996, <https://www.cs.umd.edu/~ben/papers/Shneiderman1996eyes.pdf>.
- **One screen, monitored at a glance, color held for exceptions.** Few defines a dashboard as information "consolidated and arranged on a single screen so the information can be monitored at a glance," providing "an easy gateway to any additional information." He warns that "When everything is visually prominent, nothing stands out," and that overused color "loses its power to highlight what's most important." Few, "Common Pitfalls in Dashboard Design," 2005, <https://www.perceptualedge.com/articles/Whitepapers/Common_Pitfalls.pdf>. His book *Information Dashboard Design* was not checked.
- **Change since the last look, at word size.** A sparkline is "a small intense, simple, word-sized graphic with typographic resolution" (Tufte, *Beautiful Evidence*, 2006, pp. 46–63, via <https://www.edwardtufte.com/notebook/sparkline-theory-and-practice-edward-tufte/>). Small multiples answer "Compared to what?" by repeating one design across items (*Envisioning Information*, 1990, quoted via Wikipedia, not checked against the book). Together they cover change over time per workstream and comparison across workstreams.
- **Calm at the periphery.** Weiser and Brown describe technology that "will move easily from the periphery of our attention, to the center, and back." On a normal day the dashboard stays peripheral; a critical item brings it to the center. "Designing Calm Technology," 1995, <https://calmtech.com/papers/designing-calm-technology>.

### Workstreams and staleness

- **One lane per workstream.** LifeLines draws each facet of a record as a horizontal line with events marked on it, in one overview screen. A workstream that has gone quiet is a lane whose marks stop. Plaisant et al., CHI 1996, <https://drum.lib.umd.edu/bitstream/1903/440/2/CS-TR-3523.pdf> (taken from abstracts; the paper itself was not checked).
- **Age and staleness are separate measures.** The Kanban Guide defines Work Item Age as "the elapsed time between when a work item started and the current date" (<https://kanbanguides.org/english/>). Time since last touched is a different measure: a workstream can be young and stale, or old and active. Show both. The aging work-in-progress chart — one dot per item, age against workflow stage, with percentile threshold lines — is a ready-made layout for the staleness view, with time since last touched on the age axis (<https://55degrees.atlassian.net/wiki/spaces/AAS/pages/2454290433/Aging+Work+in+Progress>).
- **Work in progress against a limit.** Little's Law relates average work in progress to throughput and cycle time, holding where arrivals and departures roughly balance and work that starts eventually finishes (<https://businessmap.io/continuous-flow/littles-law>). Under those conditions, more concurrent workstreams lengthen cycle time unless throughput rises, which is the hypothesis this dashboard tests — so a count of active workstreams belongs beside a limit the user sets. Vacanti's own statement of it was not checked.
- **The list stays visible, and each stream keeps its state.** González and Mark observed people working in "an average of ten different working spheres" a day, keeping "their list of current tasks always visible," and recommend tools that "save the state of the information device particular to that working sphere." CHI 2004, <https://www.ics.uci.edu/~gmark/CHI2004.pdf>.
- **A cue recorded at suspension.** Altmann and Trafton's memory-for-goals model holds that a suspended goal "can only be retrieved with help from priming from some associated cue," available both when the goal is suspended and when it is resumed. This is the basis for the "where I left off" cue. *Cognitive Science* 26, 2002, <https://www.interruptions.net/literature/Altmann-CogSci02.pdf>.

### Interrupts

- **Interruptions cost more than they appear to.** Interrupted tasks finished faster at no loss of quality, but with "more stress, higher frustration, time pressure and effort," and a related interruption was no cheaper than an unrelated one. Mark, Gudith and Klocke, CHI 2008, <https://www.ics.uci.edu/~gmark/chi08-mark.pdf>.
- **Paused work stays visible.** Alert-driven switches cost "nearly 10 minutes," then "another 10 to 15 minutes" to return to focused work, and suspended windows less than 25% visible took significantly longer to resume. Iqbal and Horvitz, CHI 2007, <https://www.erichorvitz.com/CHI_2007_Iqbal_Horvitz.pdf>.
- **Only actionable items interrupt.** "Every page should be actionable," and "When pages occur too frequently, employees second-guess, skim, or even ignore incoming alerts." Google SRE book, ch. 6, <https://sre.google/sre-book/monitoring-distributed-systems/>.

Together these give **two levels of loudness**. Only stop-the-line items take focus. Questions awaiting an answer sit quietly in the critical-items panel, and paused workstreams stay visible as cues for resuming them. Critical items get their own panel, apart from the ranking: sharing a list, either the interrupts get buried or they displace the ranking.

## Renderer candidates

Not chosen. Each would read lookout's readout, never the sources behind it.

- **Grafana OSS with the Infinity data source** (<https://grafana.com/docs/plugins/yesoreyeram-infinity-datasource/latest/>). The State timeline panel draws lanes, and dashboards and data sources are provisioned from files. A panel's "No data" stays quiet, so the health record would need generated alert rules or a health panel.
- **Observable Framework.** Data loaders in any language, Plot or D3 charts, a static site. A cached loader result can be served stale without notice (<https://github.com/observablehq/framework/issues/1148>), so freshness is the data layer's to enforce.
- **A claude.ai Artifact, with a Claude Code mod as the cue.** The Artifact draws the graphical view; the mod puts critical items and stale lanes above the prompt, where attention already is. An Artifact reaches local MCP servers, and so a readout served by one, only when opened in the Claude desktop app, and putting work data into a page hosted under one account raises the question of which accounts may hold that data.

Backstage is too heavy for one person; Homepage, Glance and Dashy offer no general-purpose charting over custom data.

## Open strategic questions

Preserved as open. Each names what would settle it.

### Who keeps the configuration current?

A configuration file that has to be edited by hand goes stale the way a hand-maintained planning page does. Discovery proposing changes moves the cost to a decision at the moment of appearance, but whether that is cheaper than the drift it prevents is unknown.

**What would settle it:** running the data layer against real activity and counting how often unplaced activity appears, and how often adopting it is one gesture.

### Which renderer?

The candidates differ on lanes without chart code, on the absent-versus-degraded line, on footprint, and on whether the cue can sit inside Claude Code.

**What would settle it:** the data layer's actual output shape, and which candidate renders the health states without a second configuration system kept in sync by hand.

### How are work sources read?

A setting may let a standalone tool hold its own token for its tracker and chat, or allow access only through an interactive AI session whose connector holds the credentials. Supplied files accept either route: a supplier can run with its own credentials, or inside such a session. What stays open is where the credentials sit with respect to the account that owns the data, and which accounts the readout may then reach — a renderer hosted under a different account, for one.

**What would settle it:** the setting's own rules on which accounts may hold its data, applied to this case deliberately, not inherited from whichever renderer is easiest.

### Where are stop-the-line signals recorded?

Item 3 lists them, but nothing yet records a stop-the-line signal anywhere an adapter could read, with the severity that separates a critical area from the rest.

**What would settle it:** naming a record — an existing store or a new one — before the adapter that reads it.

### How do dated obligations reach the dashboard?

Item 3 lists time-critical tasks "tracked or not", but every input a configuration declares carries activity — sessions, branches, pull requests, tickets, issues. A dated obligation a project keeps in its own files — a renewal, a review that falls due, a credential to rotate — is none of these, and a personal setting, which often has no tracker, is where such obligations most often live.

Any answer keeps the coupling loose in both directions: the dashboard never reads a project's own record format, and a project never shapes its records around lookout. That is the data-layer decision applied across a repository boundary — the project supplies reduced facts (what falls due, when, and whether the reading succeeded) and the dashboard renders them. Two routes fit: a supplied file the project writes, which the configuration locates; or an input the dashboard already reads, such as an issue the project files when something falls due, which adds no input but couples to GitHub and inherits the filing cadence.

**What would settle it:** wiring one project's dated obligations through a route, and checking whether a failed reading on the project's side reaches the dashboard as an error or degraded input rather than as nothing due.

### What throughput measure does a work-in-progress limit set against?

Resolved estimate points undercount effort that never reached an estimate, so a limit derived from them starts low, and a limit derived from nothing is a guess.

**What would settle it:** choosing a measure the data layer can observe directly — completed workstreams, merged pull requests, or resolved points beside an acknowledged undercount — and watching whether it moves as concurrency changes.

## What would falsify this

The load-bearing claim is that staleness, not capacity alone, caps throughput — that keeping concurrent workstreams from going quiet lets more of them complete. If, with the dashboard in use and staleness visibly lower, the rate of completion holds flat while the count of concurrent workstreams rises, the extra concurrency is lengthening cycle time rather than adding throughput, and the lever is the work-in-progress limit rather than awareness.
