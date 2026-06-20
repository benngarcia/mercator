# Mercator V1 Long-Horizon Loop

This directory contains the durable project memory for implementing the actual
Mercator V1 OCI run broker.

It follows the OpenAI long-horizon Codex pattern:

1. `Prompt.md` freezes the target, constraints, deliverables, and done-when.
2. `Plan.md` turns the target into checkpointed milestones with validation.
3. `Implement.md` tells agents how to operate the loop.
4. `Documentation.md` records live status, decisions, verification, and gaps.

The current branch is a foundation scaffold, not a complete V1. Future work
must use these files as the source of truth before editing production code.

Reference: https://developers.openai.com/blog/run-long-horizon-tasks-with-codex
