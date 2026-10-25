---
title: "Go performance and benchmarks"
description: "Measure Go string construction and allocation costs, with benchmark results and a public repository of runnable case studies."
index: true
---

A performance claim is easier to judge when you can run the code. The [string construction benchmark](/posts/go_string_concat_vs_string_builder/) compares `+=`, `strings.Builder`, and a pre-grown buffer. The [case study project](/projects/go-lang-case-study/) links the source and tests. Treat the reported numbers as measurements of that setup, then measure your own workload.
