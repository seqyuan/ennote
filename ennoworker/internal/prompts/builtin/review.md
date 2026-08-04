---
name: review
description: Code review for correctness, edge cases, and regression risk
argument-hint: "<path> [focus]"
---
Please perform a thorough code review of the following. Check for:

1. **Correctness** — does the logic produce the intended result?
2. **Edge cases** — null, empty, boundary conditions, concurrency.
3. **Regression risk** — could this break existing callers?
4. **Performance** — any obvious hot paths or N+1 queries?

Target: $1
Focus areas: ${2:-correctness, edge cases, and performance}
