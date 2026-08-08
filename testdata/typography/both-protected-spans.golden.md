# Protected spans

Inline code is immune: `printf("hi...")` and `it's fine` keep straight quotes and dots.

Link text and destinations are immune: [the "docs" page](https://example.com/a...b) stays as written.

Autolinks stay put: <https://example.com/x?q="y"> and so does inline math $a'b...c$.

An inline HTML tag’s attributes are immune: <span class="note">but this “prose” is not</span>.

A Hugo shortcode is immune: {{< ref "page...md" >}} and so is an MDX {expr with "quotes"} span.

A bare GFM autolink has no delimiters at all, and is immune too: http://example.com/a"b...c stays byte-for-byte.

Footnote references are immune too: see the note[^1] for details.

[^1]: A footnote body is ordinary prose, so “quotes” here do get curled…
