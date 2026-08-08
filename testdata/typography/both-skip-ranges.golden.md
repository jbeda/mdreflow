---
title: "Front matter isn't prose..."
---

# Skip ranges

A fenced code block never reaches the reflow pipeline, so nothing in it changes:

```go
fmt.Println("hello...")  // it's untouched
```

An indented code block is the same:

    echo "don't touch me..."

A table is a skip block:

| Column | Note |
|---|---|
| `x` | "quoted... value" |

But ordinary prose around them is reflowed and curled: “like this…” right here.
