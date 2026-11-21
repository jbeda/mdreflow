With a blank line between the tag and the fence, CommonMark reads the fence too. The prose after it reflows as usual.

<Playground>

```css
.a {
  color: red;
}

.b {
  color: blue;
}
```

</Playground>

Both parsers agree on this layout. Nothing here is frozen.
