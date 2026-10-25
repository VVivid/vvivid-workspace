---
title: "Integration debugging: time, money, and retries"
description: "Practical integration failures involving MongoDB timezones, fractional cents, and payment retries, with debugging steps and explicit data rules."
---

Integrations fail when two systems give the same value different meanings. A timestamp becomes a local calendar date. An order total cannot be divided into equal whole-cent unit prices. A timeout hides a payment that already succeeded.

## Choose the boundary you are debugging

- [Go and MongoDB timezone mismatch](/posts/the_silent_timezone_mismatch_go_vs_mongodb/): inspect the instant and location, then choose the timezone before formatting a date.
- [Fractional cents in a POS integration](/posts/handling_fractional_cents_integration/): preserve the total by representing the remainder explicitly.
- [Judging generated retry logic](/posts/the_trap_of_vibe_coding_for_juniors/): use payment idempotency to question whether a retry is safe.

The useful question is often simple: which assumption does each side make, and where is that assumption written down? My [backend and platform work](/about/) is largely about making those boundaries visible.
