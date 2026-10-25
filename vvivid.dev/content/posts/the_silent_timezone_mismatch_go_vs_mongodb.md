---
title: "The Silent Timezone Mismatch: Go vs MongoDB"
date: 2026-01-16T18:33:20+01:00
lastmod: 2026-10-25
author: "Ou David"
draft: false
tags: ["golang", "debugging", "mongodb", "distributed-systems"]
categories: ["Technical Deep Dives"]
description: "How implicit conversions between primitive.DateTime and time.Time can cause subtle bugs."
toc: false
math: false
---

A BSON timestamp represents an instant, but formatting it as a calendar date requires a timezone. In the `primitive.DateTime.Time()` path described here, Go's local location moved the formatted date across midnight. Converting to the intended timezone before `Format(time.DateOnly)` fixed our UTC-date comparison.


I recently encountered a subtle bug that cost more time than I'd like to admit. It’s a classic example of how implicit defaults in our tools can lead to unexpected behavior in distributed systems.

## A payment date comparison fails across midnight {#the-real-problem}

I was implementing a validation function, `isValidCiaoPayment`. The logic was simple: check if a payment record from MongoDB matches a specific check date (e.g., "2026-01-15").

Here is the logic:

```go
// Convert payment time to string YYYY-MM-DD
paymentUpdatedAtDate := payment.UpdatedAt.Time().Format(time.DateOnly)

// Compare with the target date
if paymentUpdatedAtDate != checkDate {
    return false 
}
```

The function kept returning `false` even when the data looked correct in the database.

## A BSON instant does not choose a calendar timezone {#the-wrong-assumption}

The wrong assumption was that calling `.Time().Format(time.DateOnly)` would preserve the same date boundary that we saw in MongoDB.

The core issue lies in how `primitive.DateTime` crosses into Go's `time.Time`.

*   **MongoDB (BSON)** uses `primitive.DateTime`, which is essentially a 64-bit integer representing milliseconds since the Unix epoch (UTC). It has no timezone information.
*   **Go** uses `time.Time`, which *does* contain location (timezone) information.

When you call `.Time()` on a `primitive.DateTime`, the method has to decide what Location to assign to the new `time.Time` struct.

For `primitive.DateTime.Time()`, that conversion uses Go's `time.Unix`, which returns a time in **Local**.

So:
1.  **MongoDB**: `2026-01-15 17:00:00 +0000 UTC` (Stored purely as epoch milliseconds).
2.  **Go Conversion**: `.Time()` reads the epoch, creates a `time.Time`, and attaches your server's `Local` location.
3.  **Result**: `2026-01-16 00:00:00 +0700 +07` (If you are in UTC+7).

## Local time moves the formatted date to the next day {#the-failure-mode}

Since 5:00 PM UTC became 12:00 AM the *next day* in local time, calling `.Format(time.DateOnly)` resulted in "2026-01-16", causing the mismatch against "2026-01-15".

This bug is easy to miss because both values describe the same instant. The problem is not the timestamp. The problem is formatting that timestamp into a calendar date before deciding which timezone owns the date boundary.

## Log the instant, location, and both calendar dates {#the-debugging-path}

The useful debugging step was to print both the instant and the location attached to the `time.Time` value:

```go
t := payment.UpdatedAt.Time()
log.Printf(
    "updated_at=%s location=%s utc_date=%s local_date=%s",
    t.Format(time.RFC3339),
    t.Location(),
    t.UTC().Format(time.DateOnly),
    t.Format(time.DateOnly),
)
```

That made the mismatch visible. The UTC date was still `2026-01-15`, but the local formatted date had moved to `2026-01-16`.

## Why primitive.DateTime.Time() can return Local {#why-does-the-driver-do-this}

It’s easy to blame the driver, but it is actually respecting Go's standard library design.

In Go, the `time.Unix(sec, nsec)` function returns a `time.Time` with the **Local** location. 

```go
// From Go standard library documentation
// Unix returns the local Time corresponding to the given Unix time,
// sec seconds and nsec nanoseconds since January 1, 1970 UTC.
func Unix(sec int64, nsec int64) Time
```

That means this code:

```go
payment.UpdatedAt.Time()
```

can produce a local-time value even though the original BSON value represents a UTC instant.

There is one important nuance: decoding BSON directly into a `time.Time` struct field is different. The MongoDB Go driver defaults decoded `time.Time` values to UTC unless local timezone decoding is enabled. This article is specifically about the `primitive.DateTime.Time()` path.

The driver team arguably had two choices for `primitive.DateTime.Time()`:
1.  **Force UTC**: This is "technically" more pure for a database driver but breaks Go's convention.
2.  **Respect Go Standard Lib**: Follow `time.Unix` behavior and return Local time.

They chose option #2 for this method. It wasn't an oversight; it follows Go's standard library behavior. If Go's own `time.Unix` constructor returns Local time, the method doing the same thing is consistent, even if it causes headaches for backend engineers expecting UTC by default.

## Choose UTC before formatting the payment date {#the-fix}

In backend engineering, we care about absolute time. We shouldn't rely on implicit conversions that assume the server's local clock matters for data logic.

The fix is to explicitly force the Location back to UTC before formatting or comparing:

```go
// Explicitly convert to UTC to strip the Local location bias
paymentUpdatedAtDate := payment.UpdatedAt.Time().UTC().Format(time.DateOnly)
```

## Make the date boundary explicit {#takeaway}

If you are working with `primitive.DateTime`, remember that it is just a timestamp. When it crosses the boundary into `time.Time`, ensure you control the Timezone context, or defaults might bite you.

## Related articles

For another mismatch between integration data models, see [fractional cents in a POS integration](/posts/handling_fractional_cents_integration/). Both examples are collected in the [integration debugging guide](/topics/integration-debugging/).
