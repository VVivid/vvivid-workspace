---
title: "Handling Fractional Cents in Integrations"
date: 2026-01-21T08:48:01+01:00
author: "Ou David"
draft: false
tags: ["system-design", "money", "integration", "software-architecture"]
categories: ["Technical Deep Dives"]
description: "How a design flaw in POS integration led to a 0.01 Euro discrepancy and how we solved it."
toc: false
math: true
---

I'm back with another story about a "silent error" in our systems. In my [previous post](/posts/the_silent_timezone_mismatch_go_vs_mongodb), I talked about how implicit timezone conversions can bite you. Today, let's talk about a different type of error: a design flaw when dealing with money.

### The Real Problem

We are building a Central Reservation System (CRS) for a tourist park. One of our golden rules when dealing with money is simple: **always use integers**. We convert everything to cents before pushing it to our cash-register API.

For example, if a customer buys a buffet for 26.25 €, we treat it as 2625 cents. This has been working great, and a typical request to our cash register looks like this:

```jsonc
{
  "transactionId": "3316139954296091",
  "payments": [
    {
      "type": "CB",
      "amount": 2625
    }
  ],
  "products": [
    {
      "id": "327057",
      "name": "Adult Buffet",
      "quantity": 1,
      "unitPriceWithTax": 2625, // 26.25 €
      "totalAmount": 2625
    }
  ]
}
```

Everything was fine until we had to integrate with a new Point of Sale (POS) system for room service.

### The Wrong Assumption

The new POS system didn't speak our "integer-only" language in the way we expected. Instead of sending us a clean unit price, they sent us the **Total Included Tax** (`TotalInc`) and the **Quantity** (`Qty`). They left it up to us to figure out the unit price.

Here is what their payload looked like:

```jsonc
{
  "$type": "Sales.Export.Data.OrderLine",
  "ProductName": "Kids Buffet",
  "Qty": 2, // Quantity is 2
  "TotalInc": 26.25, // Total is 26.25 €
  "Price": 17.5 // Base price (irrelevant for tax calc here)
}
```

Do you see the problem? 

We have a total of 26.25 € for 2 items. To get the unit price for our system (which requires `unitPrice * quantity = total`), we do the math:

$$
26.25 / 2 = 13.125
$$

**13.125 cents?** That doesn't exist. We can't represent half a cent in our integer-based system.

### The Failure Mode

If we try to round it standardly:
*   **Round Down**: 13.12 € -> 1312 cents.
    *   `1312 * 2 = 2624` cents. 
    *   **Result**: We are missing 1 cent (0.01 €).
*   **Round Up**: 13.13 € -> 1313 cents.
    *   `1313 * 2 = 2626` cents.
    *   **Result**: We are over by 1 cent (0.01 €).

### The Debugging Path

When we sent these rounded values to our cash-register API, it immediately rejected the transaction. It performed a simple validation check:

```go
if sum(product.UnitPrice * product.Quantity) != payment.Amount {
    return error("Payment mismatch")
}
```

Because 26.24 != 26.25 (or 26.26 != 26.25), the payment failed. A simple 0.01 € discrepancy caused the entire transaction to be lost.

### Who is at Fault?

Is this a system design error? Absolutely. But whose?
*   **The POS System?** Maybe. Sending aggregate totals without precise unit prices assumes infinite precision or non-integer currency handling on the receiving end.
*   **Our System?** Maybe. We enforced a strict `Unit Price * Qty` model that perfectly matches real-world physical cash but struggles with abstract split bills.

This "split check" problem is common. Imagine 3 people splitting a 10 € bill. 3.33 + 3.33 + 3.33 = 9.99. Who pays the extra cent?

### The Fix: The "Remainder" Item

We can't change the POS system, and we can't change the laws of math. So, we changed how we represent the order.

Instead of declaring "2 items at X price", we split the line item. We calculate the floor price for all items, and then add the remainder to one of them.

**The Algorithm:**

1.  Calculate base unit price: `floor(2625 / 2) = 1312` cents.
2.  Calculate the remainder: `2625 - (1312 * 2) = 1` cent.
3.  Split the item into `(Qty - 1)` items at the base price, and `1` item at `base price + remainder`.

So our request to the cash register became:

```jsonc
"products": [
  {
    "name": "Kids Buffet (1/2)",
    "quantity": 1,
    "unitPriceWithTax": 1312 // 13.12 €
  },
  {
    "name": "Kids Buffet (2/2)",
    "quantity": 1,
    "unitPriceWithTax": 1313 // 13.13 € (includes the remainder)
  }
]
```

Total: `1312 + 1313 = 2625`. 
It matches exactly. Validated. Paid.

### Takeaway

Dealing with money is never just about `float` vs `int` (though please, always use `int` or `decimal`). It's about how your data structure represents reality. In reality, you can't pay 13.125 cents. You have to pay 13 cents or 14 cents.

When integrating systems, never assume that `Total / Qty` yields a clean number. Always design for the remainder.
